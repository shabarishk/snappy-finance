package main

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type financeEvent struct {
	EventID          string  `json:"event_id"`
	Name             string  `json:"name"`
	StartDate        string  `json:"start_date"`
	EndDate          string  `json:"end_date"`
	TransactionCount int     `json:"transaction_count"`
	TotalExpenses    float64 `json:"total_expenses"`
}

type eventTransaction struct {
	TransactionID    string  `json:"transaction_id"`
	AccountID        string  `json:"account_id"`
	AccountName      string  `json:"account_name"`
	Name             string  `json:"name"`
	MerchantName     *string `json:"merchant_name"`
	Amount           float64 `json:"amount"`
	Date             string  `json:"date"`
	Currency         *string `json:"iso_currency_code"`
	CategoryID       *string `json:"category_id"`
	CategoryLabel    *string `json:"category_label"`
	SubcategoryLabel *string `json:"subcategory_label"`
	Included         bool    `json:"included"`
}

func (a *app) listEvents(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `
		SELECT event.event_id, event.name, event.start_date::text, event.end_date::text,
			COUNT(txn.transaction_id) FILTER (
				WHERE membership.included AND ($1 OR NOT txn.hidden_from_users)
			)::int,
			COALESCE(SUM(txn.amount) FILTER (
				WHERE membership.included AND ($1 OR NOT txn.hidden_from_users)
			), 0)
		FROM events event
		LEFT JOIN event_transactions membership ON membership.event_id=event.event_id
		LEFT JOIN application_transactions txn ON txn.transaction_id=membership.transaction_id
		GROUP BY event.event_id
		ORDER BY event.start_date DESC, event.created_at DESC`,
		currentPrincipal(r).Role == "admin")
	if err != nil {
		log.Printf("query events: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load events")
		return
	}
	defer rows.Close()
	events := make([]financeEvent, 0)
	for rows.Next() {
		var event financeEvent
		if err := rows.Scan(&event.EventID, &event.Name, &event.StartDate, &event.EndDate,
			&event.TransactionCount, &event.TotalExpenses); err != nil {
			log.Printf("scan event: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load events")
			return
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		log.Printf("iterate events: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load events")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (a *app) createEvent(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name      string `json:"name"`
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid event details")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if utf8.RuneCountInString(input.Name) < 1 || utf8.RuneCountInString(input.Name) > 100 {
		writeError(w, http.StatusBadRequest, "event name must be between 1 and 100 characters")
		return
	}
	startDate, err := time.Parse("2006-01-02", input.StartDate)
	if err != nil || startDate.Format("2006-01-02") != input.StartDate {
		writeError(w, http.StatusBadRequest, "start_date must be a valid YYYY-MM-DD date")
		return
	}
	endDate, err := time.Parse("2006-01-02", input.EndDate)
	if err != nil || endDate.Format("2006-01-02") != input.EndDate || endDate.Before(startDate) {
		writeError(w, http.StatusBadRequest, "end_date must be a valid date on or after start_date")
		return
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		log.Printf("begin event creation: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create event")
		return
	}
	defer tx.Rollback(r.Context())
	var event financeEvent
	if err := tx.QueryRow(r.Context(), `
		INSERT INTO events (name, start_date, end_date)
		VALUES ($1, $2, $3)
		RETURNING event_id, name, start_date::text, end_date::text`,
		input.Name, startDate, endDate,
	).Scan(&event.EventID, &event.Name, &event.StartDate, &event.EndDate); err != nil {
		log.Printf("insert event: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create event")
		return
	}
	_, err = tx.Exec(r.Context(), `
		INSERT INTO event_transactions (event_id, transaction_id)
		SELECT $1, txn.transaction_id
		FROM application_transactions txn
		JOIN accounts account ON account.account_id=txn.account_id
		LEFT JOIN transaction_categories category ON category.category_id=txn.category_id
		LEFT JOIN transaction_categories parent_category ON parent_category.category_id=category.parent_category_id
		WHERE txn.transaction_date BETWEEN $2 AND $3
		  AND txn.amount > 0
		  AND txn.pending=FALSE
		  AND txn.hidden_from_users=FALSE
		  AND NOT account.excluded_from_calculations
		  AND COALESCE(category.category_type, 'expense')='expense'
		  AND COALESCE(txn.category_id, '') NOT IN ('transfer', 'transfer_expense')
		  AND NOT COALESCE(category.excluded_from_totals OR parent_category.excluded_from_totals, FALSE)
		  AND NOT EXISTS (
			  SELECT 1 FROM subscription_payment_history history
			  WHERE history.transaction_id=txn.transaction_id
		  )
		  AND NOT EXISTS (
			  SELECT 1
			  FROM scheduled_projection_expenses expense
			  WHERE expense.active
			    AND (expense.account_id IS NULL OR expense.account_id=txn.account_id)
			    AND EXISTS (
				    SELECT 1
				    FROM unnest(expense.merchant_names || ARRAY[expense.merchant_name, expense.name])
				        AS scheduled(scheduled_merchant)
				    WHERE NULLIF(BTRIM(scheduled_merchant), '') IS NOT NULL
				      AND regexp_replace(lower(scheduled_merchant), '[^a-z0-9]', '', 'g')
				          = regexp_replace(lower(COALESCE(NULLIF(txn.merchant_name, ''), txn.name)), '[^a-z0-9]', '', 'g')
			    )
		  )
		ON CONFLICT (event_id, transaction_id) DO NOTHING`,
		event.EventID, startDate, endDate)
	if err != nil {
		log.Printf("select event expenses: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create event")
		return
	}
	if err := tx.QueryRow(r.Context(), `
		SELECT COUNT(*)::int, COALESCE(SUM(txn.amount), 0)
		FROM event_transactions membership
		JOIN application_transactions txn ON txn.transaction_id=membership.transaction_id
		WHERE membership.event_id=$1 AND membership.included`,
		event.EventID,
	).Scan(&event.TransactionCount, &event.TotalExpenses); err != nil {
		log.Printf("summarize event expenses: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("commit event creation: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create event")
		return
	}
	writeJSON(w, http.StatusCreated, event)
}

func (a *app) getEvent(w http.ResponseWriter, r *http.Request) {
	event, err := a.readEvent(r, r.PathValue("eventID"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		log.Printf("load event: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load event")
		return
	}
	writeJSON(w, http.StatusOK, event)
}

func (a *app) readEvent(r *http.Request, eventID string) (financeEvent, error) {
	var event financeEvent
	err := a.db.QueryRow(r.Context(), `
		SELECT event.event_id, event.name, event.start_date::text, event.end_date::text,
			COUNT(txn.transaction_id) FILTER (
				WHERE membership.included AND ($2 OR NOT txn.hidden_from_users)
			)::int,
			COALESCE(SUM(txn.amount) FILTER (
				WHERE membership.included AND ($2 OR NOT txn.hidden_from_users)
			), 0)
		FROM events event
		LEFT JOIN event_transactions membership ON membership.event_id=event.event_id
		LEFT JOIN application_transactions txn ON txn.transaction_id=membership.transaction_id
		WHERE event.event_id=$1
		GROUP BY event.event_id`, eventID, currentPrincipal(r).Role == "admin",
	).Scan(&event.EventID, &event.Name, &event.StartDate, &event.EndDate,
		&event.TransactionCount, &event.TotalExpenses)
	return event, err
}

func (a *app) listEventTransactions(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventID")
	event, err := a.readEvent(r, eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		log.Printf("load event transactions: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load event transactions")
		return
	}
	fromDate, toDate, err := parseTransactionDateRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if fromDate == "" {
		fromDate = event.StartDate
	}
	if toDate == "" {
		toDate = event.EndDate
	}
	rows, err := a.db.Query(r.Context(), `
		SELECT txn.transaction_id, txn.account_id, COALESCE(account.official_name, account.name),
			txn.name, txn.merchant_name, txn.amount, txn.transaction_date::text,
			txn.iso_currency_code, txn.category_id, COALESCE(parent_category.label, category.label),
			CASE WHEN category.parent_category_id IS NOT NULL THEN category.label END,
			COALESCE(membership.included, FALSE)
		FROM application_transactions txn
		JOIN accounts account ON account.account_id=txn.account_id
		LEFT JOIN transaction_categories category ON category.category_id=txn.category_id
		LEFT JOIN transaction_categories parent_category ON parent_category.category_id=category.parent_category_id
		LEFT JOIN event_transactions membership
		  ON membership.event_id=$1 AND membership.transaction_id=txn.transaction_id
		WHERE txn.amount > 0
		  AND txn.pending=FALSE
		  AND COALESCE(category.category_type, 'expense')='expense'
		  AND COALESCE(txn.category_id, '') NOT IN ('transfer', 'transfer_expense')
		  AND ($2 OR NOT txn.hidden_from_users)
		  AND (membership.included=TRUE OR txn.transaction_date BETWEEN $3::date AND $4::date)
		ORDER BY txn.transaction_date DESC, txn.transaction_id`,
		eventID, currentPrincipal(r).Role == "admin", fromDate, toDate)
	if err != nil {
		log.Printf("query event transactions: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load event transactions")
		return
	}
	defer rows.Close()
	transactions := make([]eventTransaction, 0)
	for rows.Next() {
		var item eventTransaction
		if err := rows.Scan(&item.TransactionID, &item.AccountID, &item.AccountName, &item.Name,
			&item.MerchantName, &item.Amount, &item.Date, &item.Currency, &item.CategoryID,
			&item.CategoryLabel, &item.SubcategoryLabel, &item.Included); err != nil {
			log.Printf("scan event transaction: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load event transactions")
			return
		}
		transactions = append(transactions, item)
	}
	if err := rows.Err(); err != nil {
		log.Printf("iterate event transactions: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load event transactions")
		return
	}
	writeJSON(w, http.StatusOK, transactions)
}

func (a *app) setEventTransactionIncluded(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Included *bool `json:"included"`
	}
	if err := decodeJSON(r, &input); err != nil || input.Included == nil {
		writeError(w, http.StatusBadRequest, "included is required")
		return
	}
	eventID, transactionID := r.PathValue("eventID"), r.PathValue("transactionID")
	var exists bool
	err := a.db.QueryRow(r.Context(), `
		SELECT EXISTS (
			SELECT 1
			FROM events event
			JOIN application_transactions txn ON txn.transaction_id=$2
			LEFT JOIN transaction_categories category ON category.category_id=txn.category_id
			WHERE event.event_id=$1 AND txn.amount > 0 AND txn.pending=FALSE
			  AND COALESCE(category.category_type, 'expense')='expense'
			  AND COALESCE(txn.category_id, '') NOT IN ('transfer', 'transfer_expense')
			  AND ($3 OR NOT txn.hidden_from_users)
		)`, eventID, transactionID, currentPrincipal(r).Role == "admin").Scan(&exists)
	if err != nil {
		log.Printf("validate event transaction: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update event transaction")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "event or expense not found")
		return
	}
	_, err = a.db.Exec(r.Context(), `
		INSERT INTO event_transactions (event_id, transaction_id, included)
		VALUES ($1, $2, $3)
		ON CONFLICT (event_id, transaction_id)
		DO UPDATE SET included=EXCLUDED.included`,
		eventID, transactionID, *input.Included)
	if err != nil {
		log.Printf("save event transaction membership: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update event transaction")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"included": *input.Included})
}
