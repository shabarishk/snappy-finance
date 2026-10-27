package main

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type subscriptionTrackerPayment struct {
	ExpenseID       int64    `json:"expense_id"`
	Name            string   `json:"name"`
	MerchantName    string   `json:"merchant_name"`
	AccountName     string   `json:"account_name"`
	DueDate         string   `json:"due_date"`
	Amount          float64  `json:"amount"`
	TrackingMode    string   `json:"tracking_mode"`
	Paid            bool     `json:"paid"`
	ActualAmount    *float64 `json:"actual_amount"`
	PaymentDate     *string  `json:"payment_date"`
	TransactionID   *string  `json:"transaction_id"`
	PendingReviewID *int64   `json:"pending_review_id"`
}

type subscriptionTrackerMonth struct {
	SpendMonth                string                       `json:"spend_month"`
	Payments                  []subscriptionTrackerPayment `json:"payments"`
	ScheduledExpenseTotal     float64                      `json:"scheduled_expense_total"`
	PaidScheduledExpenseTotal float64                      `json:"paid_scheduled_expense_total"`
}

type subscriptionPaymentHistory struct {
	Name        string  `json:"name"`
	Merchant    string  `json:"merchant_name"`
	Account     string  `json:"account_name"`
	DueDate     string  `json:"due_date"`
	PaymentDate string  `json:"payment_date"`
	Amount      float64 `json:"amount"`
}

type subscriptionAmountHistory struct {
	Name          string  `json:"name"`
	Amount        float64 `json:"amount"`
	EffectiveDate string  `json:"effective_date"`
	ChangeType    string  `json:"change_type"`
}

func (a *app) getSubscriptionTracker(w http.ResponseWriter, r *http.Request) {
	monthKey := r.URL.Query().Get("spend_month")
	startMonth, err := time.Parse("2006-01", monthKey)
	if err != nil || startMonth.Format("2006-01") != monthKey {
		writeError(w, http.StatusBadRequest, "invalid spend month")
		return
	}
	startMonth = time.Date(startMonth.Year(), startMonth.Month(), 1, 0, 0, 0, 0, time.UTC)
	_, periodEnd, monthKeys := subscriptionTrackerPeriod(startMonth)
	occurrenceStart := startMonth.AddDate(0, -1, 0)
	paymentsByMonth := make(map[string][]subscriptionTrackerPayment, 3)
	for _, monthKey := range monthKeys {
		paymentsByMonth[monthKey] = make([]subscriptionTrackerPayment, 0)
	}

	expenses, err := a.loadScheduledExpenses(r.Context())
	if err != nil {
		log.Printf("load subscription tracker schedules: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load subscription tracker")
		return
	}

	for _, expense := range expenses {
		dueDate, err := time.Parse("2006-01-02", expense.FirstDueDate)
		if err != nil {
			log.Printf("parse scheduled expense date for tracker: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load subscription tracker")
			return
		}
		accountName, accountType := "", ""
		var closeDay, dueDay *int16
		if expense.AccountID != nil {
			if err := a.db.QueryRow(r.Context(),
				`SELECT COALESCE(account.official_name, account.name), account.type,
					cycle.statement_close_day, cycle.statement_due_day
				FROM accounts account
				LEFT JOIN credit_card_cycles cycle USING (account_id)
				WHERE account.account_id = $1`, *expense.AccountID,
			).Scan(&accountName, &accountType, &closeDay, &dueDay); err != nil {
				log.Printf("load scheduled expense account: %v", err)
				writeError(w, http.StatusInternalServerError, "could not load subscription tracker")
				return
			}
		}
		usedTransactions := make(map[string]bool)
		for _, due := range scheduledExpenseDueDates(expense.Frequency, dueDate, occurrenceStart, periodEnd) {
			spendMonth := scheduledPaymentSpendMonth(due, accountType, closeDay, dueDay)
			if _, included := paymentsByMonth[spendMonth]; !included {
				continue
			}
			payment := subscriptionTrackerPayment{
				ExpenseID:    expense.ID,
				Name:         expense.Name,
				MerchantName: expense.MerchantName,
				DueDate:      due.Format("2006-01-02"),
				Amount:       expense.Amount,
				TrackingMode: expense.TrackingMode,
				AccountName:  accountName,
			}
			if expense.AccountID == nil || expense.MerchantName == "" {
				paymentsByMonth[spendMonth] = append(paymentsByMonth[spendMonth], payment)
				continue
			}
			spendMonthDate, err := time.Parse("2006-01", spendMonth)
			if err != nil {
				log.Printf("parse scheduled payment spend month: %v", err)
				writeError(w, http.StatusInternalServerError, "could not load subscription tracker")
				return
			}
			cycleStart := time.Date(spendMonthDate.Year(), spendMonthDate.Month(), 4, 0, 0, 0, 0, time.UTC)
			cycleEnd := cycleStart.AddDate(0, 1, 0).AddDate(0, 0, -1)
			transactionStart := cycleStart
			transactionEnd := cycleEnd
			if accountType == "credit" {
				transactionStart = cycleStart.AddDate(0, -2, 0)
				transactionEnd = cycleEnd.AddDate(0, 1, 0)
			}
			rows, err := a.db.Query(r.Context(), `
				SELECT transaction_id, name, merchant_name, amount, transaction_date::text
				FROM transactions
				WHERE account_id = $1 AND transaction_date BETWEEN $2 AND $3 AND pending = FALSE
				ORDER BY ABS(transaction_date - $4), transaction_date DESC`,
				*expense.AccountID, transactionStart, transactionEnd, due)
			if err != nil {
				log.Printf("find subscription payment transactions: %v", err)
				writeError(w, http.StatusInternalServerError, "could not load subscription tracker")
				return
			}
			for rows.Next() {
				var transactionID, transactionName, paymentDate string
				var merchantName *string
				var amount float64
				if err := rows.Scan(&transactionID, &transactionName, &merchantName, &amount, &paymentDate); err != nil {
					rows.Close()
					log.Printf("scan subscription payment transaction: %v", err)
					writeError(w, http.StatusInternalServerError, "could not load subscription tracker")
					return
				}
				if usedTransactions[transactionID] {
					continue
				}
				name := transactionName
				if merchantName != nil && strings.TrimSpace(*merchantName) != "" {
					name = *merchantName
				}
				if !sameMerchant(expense.MerchantName, name) || amount <= 0 {
					continue
				}
				if expense.TrackingMode == "fixed" && !amountMatchesSchedule(expense.Amount, amount) && amount < expense.Amount {
					continue
				}
				transactionDate, err := time.Parse("2006-01-02", paymentDate)
				if err != nil {
					rows.Close()
					log.Printf("parse matched subscription payment date: %v", err)
					writeError(w, http.StatusInternalServerError, "could not load subscription tracker")
					return
				}
				transactionMonth, included := transactionSpendMonth(transactionDate, accountType, closeDay, dueDay)
				if !included || transactionMonth != spendMonth {
					continue
				}
				payment.Paid = true
				usedTransactions[transactionID] = true
				payment.ActualAmount = &amount
				payment.PaymentDate = &paymentDate
				payment.TransactionID = &transactionID
				if _, err := a.db.Exec(r.Context(), `
					INSERT INTO subscription_payment_history
						(expense_id, transaction_id, due_date, payment_date, amount)
					VALUES ($1, $2, $3, $4, $5)
					ON CONFLICT (expense_id, transaction_id) DO UPDATE SET
						due_date = EXCLUDED.due_date, payment_date = EXCLUDED.payment_date, amount = EXCLUDED.amount`,
					expense.ID, transactionID, due, transactionDate, amount); err != nil {
					rows.Close()
					log.Printf("record subscription payment history: %v", err)
					writeError(w, http.StatusInternalServerError, "could not record subscription payment")
					return
				}
				if expense.TrackingMode == "fixed" && amount > expense.Amount && !amountMatchesSchedule(expense.Amount, amount) {
					if _, err := a.db.Exec(r.Context(), `
						INSERT INTO subscription_increase_reviews
							(expense_id, transaction_id, previous_amount, observed_amount)
						VALUES ($1, $2, $3, $4)
						ON CONFLICT (expense_id, transaction_id) DO NOTHING`,
						expense.ID, transactionID, expense.Amount, amount); err != nil {
						rows.Close()
						log.Printf("create subscription increase review: %v", err)
						writeError(w, http.StatusInternalServerError, "could not record subscription increase")
						return
					}
					var reviewID int64
					err := a.db.QueryRow(r.Context(), `
						SELECT review_id FROM subscription_increase_reviews
						WHERE expense_id = $1 AND transaction_id = $2 AND status = 'pending'`,
						expense.ID, transactionID).Scan(&reviewID)
					if err == nil {
						payment.PendingReviewID = &reviewID
					} else if !errors.Is(err, pgx.ErrNoRows) {
						rows.Close()
						log.Printf("load subscription increase review: %v", err)
						writeError(w, http.StatusInternalServerError, "could not load subscription increase review")
						return
					}
				}
				break
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				log.Printf("iterate subscription payment transactions: %v", err)
				writeError(w, http.StatusInternalServerError, "could not load subscription tracker")
				return
			}
			rows.Close()
			paymentsByMonth[spendMonth] = append(paymentsByMonth[spendMonth], payment)
		}
	}

	months := make([]subscriptionTrackerMonth, 0, len(monthKeys))
	for _, monthKey := range monthKeys {
		month := subscriptionTrackerMonth{
			SpendMonth: monthKey,
			Payments:   paymentsByMonth[monthKey],
		}
		month.ScheduledExpenseTotal, month.PaidScheduledExpenseTotal = subscriptionTrackerTotals(month.Payments)
		months = append(months, month)
	}

	paymentHistory := make([]subscriptionPaymentHistory, 0)
	rows, err := a.db.Query(r.Context(), `
		SELECT expense.name, expense.merchant_name, COALESCE(account.official_name, account.name, ''),
			history.due_date::text, history.payment_date::text, history.amount
		FROM subscription_payment_history history
		JOIN scheduled_projection_expenses expense ON expense.expense_id = history.expense_id
		LEFT JOIN accounts account ON account.account_id = expense.account_id
		ORDER BY history.payment_date DESC, history.history_id DESC
		LIMIT 12`)
	if err != nil {
		log.Printf("load subscription payment history: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load subscription payment history")
		return
	}
	for rows.Next() {
		var item subscriptionPaymentHistory
		if err := rows.Scan(&item.Name, &item.Merchant, &item.Account, &item.DueDate, &item.PaymentDate, &item.Amount); err != nil {
			rows.Close()
			log.Printf("scan subscription payment history: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load subscription payment history")
			return
		}
		paymentHistory = append(paymentHistory, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("iterate subscription payment history: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load subscription payment history")
		return
	}

	amountHistory := make([]subscriptionAmountHistory, 0)
	rows, err = a.db.Query(r.Context(), `
		SELECT expense.name, history.amount, history.effective_date::text, history.change_type
		FROM subscription_amount_history history
		JOIN scheduled_projection_expenses expense ON expense.expense_id = history.expense_id
		ORDER BY history.effective_date DESC, history.history_id DESC
		LIMIT 12`)
	if err != nil {
		log.Printf("load subscription amount history: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load subscription amount history")
		return
	}
	for rows.Next() {
		var item subscriptionAmountHistory
		if err := rows.Scan(&item.Name, &item.Amount, &item.EffectiveDate, &item.ChangeType); err != nil {
			rows.Close()
			log.Printf("scan subscription amount history: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load subscription amount history")
			return
		}
		amountHistory = append(amountHistory, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("iterate subscription amount history: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load subscription amount history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"months":          months,
		"payment_history": paymentHistory,
		"amount_history":  amountHistory,
	})
}

func (a *app) resolveSubscriptionIncrease(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm bool `json:"confirm"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	reviewID, err := strconv.ParseInt(r.PathValue("reviewID"), 10, 64)
	if err != nil || reviewID < 1 {
		writeError(w, http.StatusBadRequest, "invalid subscription increase review id")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		log.Printf("begin subscription increase review: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update subscription")
		return
	}
	defer tx.Rollback(r.Context())
	var expenseID int64
	var transactionID string
	var previousAmount, observedAmount float64
	err = tx.QueryRow(r.Context(), `
		SELECT expense_id, transaction_id, previous_amount, observed_amount
		FROM subscription_increase_reviews
		WHERE review_id = $1 AND status = 'pending'
		FOR UPDATE`, reviewID).Scan(&expenseID, &transactionID, &previousAmount, &observedAmount)
	if err != nil {
		log.Printf("load pending subscription increase: %v", err)
		writeError(w, http.StatusNotFound, "pending subscription increase not found")
		return
	}
	status := "dismissed"
	if input.Confirm {
		status = "confirmed"
		if _, err := tx.Exec(r.Context(), `
			UPDATE scheduled_projection_expenses SET amount = $1 WHERE expense_id = $2`,
			observedAmount, expenseID); err != nil {
			log.Printf("confirm subscription amount increase: %v", err)
			writeError(w, http.StatusInternalServerError, "could not confirm subscription amount")
			return
		}
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO subscription_amount_history
				(expense_id, amount, effective_date, change_type)
			SELECT $1, $2, transaction_date, 'confirmed_increase'
			FROM transactions WHERE transaction_id = $3
			ON CONFLICT DO NOTHING`, expenseID, observedAmount, transactionID); err != nil {
			log.Printf("record confirmed subscription increase: %v", err)
			writeError(w, http.StatusInternalServerError, "could not record subscription amount history")
			return
		}
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE subscription_increase_reviews
		SET status = $1, resolved_at = NOW()
		WHERE review_id = $2`, status, reviewID); err != nil {
		log.Printf("resolve subscription increase review: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update subscription increase review")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("commit subscription increase review: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update subscription")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"confirmed": input.Confirm})
}

func sameMerchant(scheduled, actual string) bool {
	scheduled = normalizeMerchant(scheduled)
	actual = normalizeMerchant(actual)
	return scheduled != "" && actual != "" && (scheduled == actual || strings.HasPrefix(actual, scheduled))
}

func normalizeMerchant(value string) string {
	var normalized strings.Builder
	space := false
	for _, char := range strings.ToLower(value) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			normalized.WriteRune(char)
			space = false
		} else if !space && normalized.Len() > 0 {
			normalized.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(normalized.String())
}

func subscriptionTrackerPeriod(startMonth time.Time) (time.Time, time.Time, []string) {
	startMonth = time.Date(startMonth.Year(), startMonth.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthKeys := make([]string, 3)
	for index := range monthKeys {
		monthKeys[index] = startMonth.AddDate(0, index, 0).Format("2006-01")
	}
	return startMonth.AddDate(0, 0, 3), startMonth.AddDate(0, 3, 2), monthKeys
}

func scheduledPaymentSpendMonth(due time.Time, accountType string, closeDay, dueDay *int16) string {
	if accountType != "" {
		if spendMonth, included := transactionSpendMonth(due, accountType, closeDay, dueDay); included {
			return spendMonth
		}
	}
	return spendMonthForDate(due).Format("2006-01")
}

func subscriptionTrackerTotals(payments []subscriptionTrackerPayment) (float64, float64) {
	var scheduledTotal, paidTotal float64
	for _, payment := range payments {
		scheduledTotal += payment.Amount
		if payment.Paid {
			paidTotal += payment.Amount
		}
	}
	return scheduledTotal, paidTotal
}

func amountMatchesSchedule(expected, actual float64) bool {
	tolerance := expected * 0.01
	if tolerance < 1 {
		tolerance = 1
	}
	difference := actual - expected
	if difference < 0 {
		difference = -difference
	}
	return difference <= tolerance
}
