package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"time"
)

type liquidityCategory struct {
	Label  string  `json:"label"`
	Amount float64 `json:"amount"`
}

type liquiditySpendMonth struct {
	SpendMonth                string              `json:"spend_month"`
	Categories                []liquidityCategory `json:"categories"`
	ProjectedExpendableIncome *float64            `json:"projected_expendable_income"`
	ScheduledExpenseTotal     float64             `json:"scheduled_expense_total"`
}

type dashboardLiquidity struct {
	Months                     []liquiditySpendMonth `json:"months"`
	UnconfiguredCreditAccounts []string              `json:"unconfigured_credit_accounts"`
}

func (a *app) getDashboardLiquidity(w http.ResponseWriter, r *http.Request) {
	startMonth, err := time.Parse("2006-01", r.URL.Query().Get("spend_month"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid spend month")
		return
	}
	startMonth = time.Date(startMonth.Year(), startMonth.Month(), 1, 0, 0, 0, 0, time.UTC)
	targetMonths := make(map[string]int, 3)
	response := dashboardLiquidity{
		Months:                     make([]liquiditySpendMonth, 3),
		UnconfiguredCreditAccounts: make([]string, 0),
	}
	for i := 0; i < 3; i++ {
		month := startMonth.AddDate(0, i, 0).Format("2006-01")
		targetMonths[month] = i
		response.Months[i] = liquiditySpendMonth{
			SpendMonth: month,
			Categories: make([]liquidityCategory, 0),
		}
	}
	_, _, projectedPeriods, err := a.refreshOpenIncomeProjections(r.Context())
	if err != nil {
		log.Printf("calculate dashboard liquidity projections: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load dashboard liquidity")
		return
	}
	for _, period := range projectedPeriods {
		if len(period.SpendMonthStart) < 7 {
			continue
		}
		if index, found := targetMonths[period.SpendMonthStart[:7]]; found && index > 0 {
			response.Months[index].ProjectedExpendableIncome = period.ProjectedExpendableIncome
			response.Months[index].ScheduledExpenseTotal = period.ScheduledExpenseTotal
		}
	}

	rows, err := a.db.Query(r.Context(), `
		SELECT COALESCE(account.official_name, account.name)
		FROM accounts account
		LEFT JOIN credit_card_cycles cycle USING (account_id)
		WHERE account.type = 'credit'
		  AND (cycle.account_id IS NULL OR cycle.statement_close_day IS NULL OR cycle.statement_due_day IS NULL)
		ORDER BY account.name`)
	if err != nil {
		log.Printf("query unconfigured credit card cycles: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load dashboard liquidity")
		return
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			log.Printf("scan unconfigured credit card account: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load dashboard liquidity")
			return
		}
		response.UnconfiguredCreditAccounts = append(response.UnconfiguredCreditAccounts, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("iterate unconfigured credit card accounts: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load dashboard liquidity")
		return
	}

	isAdmin := currentPrincipal(r).Role == "admin"
	var encodedPreferences []byte
	if err := a.db.QueryRow(r.Context(),
		"SELECT dashboard_preferences FROM users WHERE id = $1", currentPrincipal(r).ID,
	).Scan(&encodedPreferences); err != nil {
		log.Printf("read dashboard liquidity preferences: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load dashboard liquidity")
		return
	}
	var preferences dashboardPreferences
	if err := json.Unmarshal(encodedPreferences, &preferences); err != nil {
		log.Printf("decode dashboard liquidity preferences: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load dashboard liquidity")
		return
	}
	rows, err = a.db.Query(r.Context(), `
		SELECT txn.transaction_date::text, txn.amount, account.type,
			cycle.statement_close_day, cycle.statement_due_day,
			category.label, txn.category_detailed, txn.category_primary
		FROM transactions txn
		JOIN accounts account USING (account_id)
		LEFT JOIN credit_card_cycles cycle USING (account_id)
		LEFT JOIN transaction_categories category ON category.category_id = txn.category_id
		WHERE txn.amount > 0
		  AND account.type IN ('depository', 'credit')
		  AND ($1 OR NOT txn.hidden_from_users)
		  AND NOT COALESCE(txn.category_id = ANY($2::text[]), FALSE)
		  AND NOT COALESCE(txn.transaction_id = ANY($3::text[]), FALSE)
		  AND NOT COALESCE(category.category_type = 'expense' AND category.excluded_from_totals, FALSE)`,
		isAdmin, preferences.ExcludedSpendCategories, preferences.ExcludedLiquidityTransactions)
	if err != nil {
		log.Printf("query dashboard liquidity transactions: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load dashboard liquidity")
		return
	}
	categoryTotals := make([]map[string]float64, len(response.Months))
	for i := range categoryTotals {
		categoryTotals[i] = make(map[string]float64)
	}
	for rows.Next() {
		var dateText, accountType string
		var amount float64
		var closeDay, dueDay *int16
		var categoryLabel, detailed, primary *string
		if err := rows.Scan(&dateText, &amount, &accountType, &closeDay, &dueDay, &categoryLabel, &detailed, &primary); err != nil {
			rows.Close()
			log.Printf("scan dashboard liquidity transaction: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load dashboard liquidity")
			return
		}
		date, err := time.Parse("2006-01-02", dateText)
		if err != nil {
			rows.Close()
			log.Printf("parse transaction date %q for dashboard liquidity: %v", dateText, err)
			writeError(w, http.StatusInternalServerError, "could not load dashboard liquidity")
			return
		}
		spendMonth, included := transactionSpendMonth(date, accountType, closeDay, dueDay)
		if !included {
			continue
		}
		index, isTargetMonth := targetMonths[spendMonth]
		if !isTargetMonth {
			continue
		}
		label := firstNonEmpty(categoryLabel, detailed, primary)
		if label == "" {
			label = "Uncategorized"
		}
		categoryTotals[index][label] += amount
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("iterate dashboard liquidity transactions: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load dashboard liquidity")
		return
	}
	for i, totals := range categoryTotals {
		for label, amount := range totals {
			response.Months[i].Categories = append(response.Months[i].Categories, liquidityCategory{
				Label: label, Amount: amount,
			})
		}
		sort.Slice(response.Months[i].Categories, func(left, right int) bool {
			if response.Months[i].Categories[left].Amount == response.Months[i].Categories[right].Amount {
				return response.Months[i].Categories[left].Label < response.Months[i].Categories[right].Label
			}
			return response.Months[i].Categories[left].Amount > response.Months[i].Categories[right].Amount
		})
	}
	writeJSON(w, http.StatusOK, response)
}

func transactionSpendMonth(date time.Time, accountType string, closeDay, dueDay *int16) (string, bool) {
	switch accountType {
	case "depository":
		return spendMonthForDate(date).Format("2006-01"), true
	case "credit":
		if closeDay == nil || dueDay == nil || *closeDay < 1 || *closeDay > 31 || *dueDay < 1 || *dueDay > 31 {
			return "", false
		}
		closeDate := dayInMonth(date.Year(), date.Month(), int(*closeDay))
		if date.After(closeDate) {
			nextMonth := date.AddDate(0, 1, 0)
			closeDate = dayInMonth(nextMonth.Year(), nextMonth.Month(), int(*closeDay))
		}
		dueDate := dayInMonth(closeDate.Year(), closeDate.Month(), int(*dueDay))
		if !dueDate.After(closeDate) {
			nextMonth := closeDate.AddDate(0, 1, 0)
			dueDate = dayInMonth(nextMonth.Year(), nextMonth.Month(), int(*dueDay))
		}
		return spendMonthForDate(dueDate).Format("2006-01"), true
	default:
		return "", false
	}
}

func spendMonthForDate(date time.Time) time.Time {
	month := time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, time.UTC)
	if date.Day() < 4 {
		return month.AddDate(0, -1, 0)
	}
	return month
}

func dayInMonth(year int, month time.Month, day int) time.Time {
	firstOfNextMonth := time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)
	lastDay := firstOfNextMonth.AddDate(0, 0, -1).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func firstNonEmpty(values ...*string) string {
	for _, value := range values {
		if value != nil && *value != "" {
			return *value
		}
	}
	return ""
}
