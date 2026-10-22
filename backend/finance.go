package main

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"
)

type dashboardMetrics struct {
	CurrentExpendableBalance  *float64 `json:"current_expendable_balance"`
	CurrentTotalCreditBalance float64  `json:"current_total_credit_balance"`
	StatementBalanceDue       *float64 `json:"statement_balance_due"`
	ProjectedExpendableIncome *float64 `json:"projected_expendable_income"`
}

type creditCardStatement struct {
	AccountID        string     `json:"account_id"`
	AccountName      string     `json:"account_name"`
	DueDate          time.Time  `json:"due_date"`
	StatementDate    *time.Time `json:"statement_date"`
	StatementBalance float64    `json:"statement_balance"`
}

func (a *app) getDashboardMetrics(w http.ResponseWriter, r *http.Request) {
	if err := a.freezeExpiredProjectedIncome(r.Context()); err != nil {
		log.Printf("freeze expired projected income: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load dashboard finance metrics")
		return
	}
	start, err := time.Parse("2006-01-02", r.URL.Query().Get("start"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid statistics start date")
		return
	}
	end, err := time.Parse("2006-01-02", r.URL.Query().Get("end"))
	if err != nil || end.Before(start) {
		writeError(w, http.StatusBadRequest, "invalid statistics end date")
		return
	}
	month, err := time.Parse("2006-01", r.URL.Query().Get("spend_month"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid spend month")
		return
	}
	periodStart := time.Date(month.Year(), month.Month(), 4, 0, 0, 0, 0, time.UTC)

	var result dashboardMetrics
	var hasStatementData bool
	err = a.db.QueryRow(r.Context(), `
		SELECT
			(SELECT account.current_balance
			 FROM finance_settings settings
			 LEFT JOIN accounts account
			   ON account.account_id = settings.expendable_cash_account_id
			 WHERE settings.setting_id = 1),
			COALESCE((SELECT SUM(current_balance) FROM accounts WHERE type = 'credit'), 0),
			CASE WHEN EXISTS (SELECT 1 FROM credit_card_statements) THEN
				COALESCE((
					SELECT SUM(statement_balance)
					FROM (
						SELECT DISTINCT ON (account_id, due_date) statement_balance
						FROM credit_card_statements
						WHERE due_date BETWEEN $1 AND $2
						ORDER BY account_id, due_date,
							CASE source WHEN 'plaid' THEN 0 ELSE 1 END
					) selected_statements
				), 0)
			ELSE NULL END,
			(SELECT amount FROM projected_expendable_income WHERE period_start = $3),
			EXISTS (SELECT 1 FROM credit_card_statements)`,
		start, end, periodStart,
	).Scan(
		&result.CurrentExpendableBalance,
		&result.CurrentTotalCreditBalance,
		&result.StatementBalanceDue,
		&result.ProjectedExpendableIncome,
		&hasStatementData,
	)
	if err != nil {
		log.Printf("read dashboard finance metrics: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load dashboard finance metrics")
		return
	}
	if !hasStatementData {
		result.StatementBalanceDue = nil
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *app) getFinanceSettings(w http.ResponseWriter, r *http.Request) {
	if err := a.freezeExpiredProjectedIncome(r.Context()); err != nil {
		log.Printf("freeze expired projected income: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load financial management settings")
		return
	}

	var response struct {
		ExpendableCashAccountID   *string               `json:"expendable_cash_account_id"`
		CurrentSpendMonthStart    time.Time             `json:"current_spend_month_start"`
		ProjectedExpendableIncome *float64              `json:"projected_expendable_income"`
		ManualStatements          []creditCardStatement `json:"manual_statements"`
		PlaidStatements           []creditCardStatement `json:"plaid_statements"`
	}
	err := a.db.QueryRow(r.Context(), `
		SELECT settings.expendable_cash_account_id,
			CASE WHEN EXTRACT(DAY FROM CURRENT_DATE) >= 4
				THEN date_trunc('month', CURRENT_DATE)::date + 3
				ELSE (date_trunc('month', CURRENT_DATE) - INTERVAL '1 month')::date + 3
			END,
			projection.amount
		FROM finance_settings settings
		LEFT JOIN projected_expendable_income projection
		  ON projection.period_start = CASE WHEN EXTRACT(DAY FROM CURRENT_DATE) >= 4
			THEN date_trunc('month', CURRENT_DATE)::date + 3
			ELSE (date_trunc('month', CURRENT_DATE) - INTERVAL '1 month')::date + 3
		  END
		WHERE settings.setting_id = 1`,
	).Scan(&response.ExpendableCashAccountID, &response.CurrentSpendMonthStart, &response.ProjectedExpendableIncome)
	if err != nil {
		log.Printf("read financial management settings: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load financial management settings")
		return
	}

	response.ManualStatements = make([]creditCardStatement, 0)
	response.PlaidStatements = make([]creditCardStatement, 0)
	rows, err := a.db.Query(r.Context(), `
		SELECT statement.account_id, COALESCE(account.official_name, account.name),
			statement.due_date, statement.statement_date, statement.statement_balance, statement.source
		FROM credit_card_statements statement
		JOIN accounts account ON account.account_id = statement.account_id
		ORDER BY statement.due_date DESC, account.name`)
	if err != nil {
		log.Printf("read credit card statements: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load credit card statements")
		return
	}
	for rows.Next() {
		var statement creditCardStatement
		var source string
		if err := rows.Scan(
			&statement.AccountID,
			&statement.AccountName,
			&statement.DueDate,
			&statement.StatementDate,
			&statement.StatementBalance,
			&source,
		); err != nil {
			rows.Close()
			log.Printf("scan credit card statement: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load credit card statements")
			return
		}
		if source == "plaid" {
			response.PlaidStatements = append(response.PlaidStatements, statement)
		} else {
			response.ManualStatements = append(response.ManualStatements, statement)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("read credit card statements: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load credit card statements")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *app) setExpendableCashAccount(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AccountID *string `json:"account_id"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if input.AccountID != nil {
		accountID := strings.TrimSpace(*input.AccountID)
		if accountID == "" {
			input.AccountID = nil
		} else {
			var exists bool
			if err := a.db.QueryRow(r.Context(),
				"SELECT EXISTS (SELECT 1 FROM accounts WHERE account_id = $1 AND type = 'depository')",
				accountID).Scan(&exists); err != nil {
				log.Printf("validate expendable cash account: %v", err)
				writeError(w, http.StatusInternalServerError, "could not validate cash account")
				return
			}
			if !exists {
				writeError(w, http.StatusBadRequest, "selected account must be a cash account")
				return
			}
			input.AccountID = &accountID
		}
	}
	if _, err := a.db.Exec(r.Context(), `
		UPDATE finance_settings
		SET expendable_cash_account_id = $1, updated_at = NOW()
		WHERE setting_id = 1`, input.AccountID); err != nil {
		log.Printf("save expendable cash account: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save expendable cash account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}

func (a *app) setProjectedExpendableIncome(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Amount *float64 `json:"amount"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if input.Amount == nil || *input.Amount < 0 {
		writeError(w, http.StatusBadRequest, "projected income must be zero or greater")
		return
	}
	if err := a.freezeExpiredProjectedIncome(r.Context()); err != nil {
		log.Printf("freeze expired projected income: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save projected income")
		return
	}
	var periodStart time.Time
	err := a.db.QueryRow(r.Context(), `
		SELECT CASE WHEN EXTRACT(DAY FROM CURRENT_DATE) >= 4
			THEN date_trunc('month', CURRENT_DATE)::date + 3
			ELSE (date_trunc('month', CURRENT_DATE) - INTERVAL '1 month')::date + 3
		END`,
	).Scan(&periodStart)
	if err != nil {
		log.Printf("read current spend month for projected income: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save projected income")
		return
	}
	result, err := a.db.Exec(r.Context(), `
		INSERT INTO projected_expendable_income (period_start, amount)
		VALUES ($1, $2)
		ON CONFLICT (period_start) DO UPDATE SET amount = EXCLUDED.amount, updated_at = NOW()
		WHERE projected_expendable_income.frozen_at IS NULL`,
		periodStart, *input.Amount)
	if err != nil {
		log.Printf("save projected expendable income: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save projected income")
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "projected income for this spend month is frozen")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}

func (a *app) saveCreditCardStatement(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AccountID        string   `json:"account_id"`
		DueDate          string   `json:"due_date"`
		StatementBalance *float64 `json:"statement_balance"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	dueDate, err := time.Parse("2006-01-02", input.DueDate)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid statement due date")
		return
	}
	if input.StatementBalance == nil || *input.StatementBalance < 0 {
		writeError(w, http.StatusBadRequest, "statement balance must be zero or greater")
		return
	}
	var isCreditAccount bool
	if err := a.db.QueryRow(r.Context(),
		"SELECT EXISTS (SELECT 1 FROM accounts WHERE account_id = $1 AND type = 'credit')",
		strings.TrimSpace(input.AccountID)).Scan(&isCreditAccount); err != nil {
		log.Printf("validate credit card statement account: %v", err)
		writeError(w, http.StatusInternalServerError, "could not validate credit account")
		return
	}
	if !isCreditAccount {
		writeError(w, http.StatusBadRequest, "selected account must be a credit account")
		return
	}
	_, err = a.db.Exec(r.Context(), `
		INSERT INTO credit_card_statements (account_id, due_date, statement_balance, source)
		VALUES ($1, $2, $3, 'manual')
		ON CONFLICT (account_id, due_date, source) DO UPDATE SET
			statement_balance = EXCLUDED.statement_balance, updated_at = NOW()`,
		strings.TrimSpace(input.AccountID), dueDate, *input.StatementBalance)
	if err != nil {
		log.Printf("save manual credit card statement: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save manual statement entry")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}

func (a *app) deleteCreditCardStatement(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AccountID string `json:"account_id"`
		DueDate   string `json:"due_date"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	dueDate, err := time.Parse("2006-01-02", input.DueDate)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid statement due date")
		return
	}
	result, err := a.db.Exec(r.Context(), `
		DELETE FROM credit_card_statements
		WHERE account_id = $1 AND due_date = $2 AND source = 'manual'`,
		strings.TrimSpace(input.AccountID), dueDate)
	if err != nil {
		log.Printf("delete manual credit card statement: %v", err)
		writeError(w, http.StatusInternalServerError, "could not delete manual statement entry")
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "manual statement entry not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (a *app) freezeExpiredProjectedIncome(ctx context.Context) error {
	_, err := a.db.Exec(ctx, `
		UPDATE projected_expendable_income
		SET frozen_at = NOW()
		WHERE frozen_at IS NULL
		  AND period_start + INTERVAL '1 month' - INTERVAL '1 day' < CURRENT_DATE`)
	return err
}
