package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const demoItemID = "demo-item-local-only"

type demoAccount struct {
	id       string
	name     string
	typeName string
	subtype  string
	balance  float64
}

type demoTransaction struct {
	id        string
	accountID string
	name      string
	merchant  string
	amount    float64
	daysAgo   int
	primary   string
	detailed  string
	pending   bool
}

func seedDemoData(ctx context.Context, db *pgxpool.Pool, encryptionKey []byte) error {
	encryptedToken, err := encrypt(encryptionKey, "demo-data-not-a-plaid-token")
	if err != nil {
		return fmt.Errorf("encrypt demo placeholder: %w", err)
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO plaid_items (item_id, access_token_encrypted, demo_data)
		VALUES ($1, $2, TRUE)
		ON CONFLICT (item_id) DO UPDATE
			SET access_token_encrypted = EXCLUDED.access_token_encrypted, demo_data = TRUE`,
		demoItemID, encryptedToken); err != nil {
		return err
	}

	accounts := []demoAccount{
		{id: "demo-checking", name: "Everyday Checking", typeName: "depository", subtype: "checking", balance: 4230.56},
		{id: "demo-savings", name: "Rainy Day Savings", typeName: "depository", subtype: "savings", balance: 12875.45},
		{id: "demo-credit-card", name: "Travel Rewards Card", typeName: "credit", subtype: "credit card", balance: 874.24},
	}
	for _, account := range accounts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO accounts (account_id, item_id, name, type, subtype, current_balance, iso_currency_code)
			VALUES ($1, $2, $3, $4, $5, $6, 'USD')
			ON CONFLICT (account_id) DO UPDATE SET
				item_id = EXCLUDED.item_id, name = EXCLUDED.name, type = EXCLUDED.type,
				subtype = EXCLUDED.subtype, current_balance = EXCLUDED.current_balance,
				iso_currency_code = EXCLUDED.iso_currency_code`,
			account.id, demoItemID, account.name, account.typeName, account.subtype, account.balance); err != nil {
			return err
		}
	}

	transactions := []demoTransaction{
		{id: "demo-txn-paycheck", accountID: "demo-checking", name: "ACME PAYROLL", merchant: "Acme Payroll", amount: -2850, daysAgo: 2, primary: "INCOME", detailed: "INCOME_WAGES"},
		{id: "demo-txn-rent", accountID: "demo-checking", name: "MONTHLY RENT", merchant: "Northside Apartments", amount: 1650, daysAgo: 4, primary: "RENT_AND_UTILITIES", detailed: "RENT"},
		{id: "demo-txn-groceries", accountID: "demo-checking", name: "WHOLE FOODS", merchant: "Whole Foods Market", amount: 96.42, daysAgo: 1, primary: "FOOD_AND_DRINK", detailed: "GROCERIES"},
		{id: "demo-txn-electric", accountID: "demo-checking", name: "CITY ELECTRIC", merchant: "City Electric", amount: 73.18, daysAgo: 6, primary: "RENT_AND_UTILITIES", detailed: "UTILITIES"},
		{id: "demo-txn-coffee", accountID: "demo-credit-card", name: "CORNER COFFEE", merchant: "Corner Coffee", amount: 5.75, daysAgo: 1, primary: "FOOD_AND_DRINK", detailed: "COFFEE"},
		{id: "demo-txn-streaming", accountID: "demo-credit-card", name: "STREAMFLIX", merchant: "Streamflix", amount: 14.99, daysAgo: 8, primary: "ENTERTAINMENT", detailed: "TV_AND_MOVIES"},
		{id: "demo-txn-dinner", accountID: "demo-credit-card", name: "PARKSIDE KITCHEN", merchant: "Parkside Kitchen", amount: 58.6, daysAgo: 3, primary: "FOOD_AND_DRINK", detailed: "RESTAURANTS"},
		{id: "demo-txn-pharmacy", accountID: "demo-checking", name: "CITY PHARMACY", merchant: "City Pharmacy", amount: 28.41, daysAgo: 5, primary: "MEDICAL", detailed: "PHARMACIES_AND_SUPPLEMENTS"},
	}
	for _, item := range transactions {
		date := time.Now().AddDate(0, 0, -item.daysAgo).Format("2006-01-02")
		if _, err := tx.Exec(ctx, `
			INSERT INTO transactions (
				transaction_id, item_id, account_id, name, merchant_name, amount,
				transaction_date, pending, category_primary, category_detailed, iso_currency_code
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'USD')
			ON CONFLICT (transaction_id) DO UPDATE SET
				account_id = EXCLUDED.account_id, name = EXCLUDED.name, merchant_name = EXCLUDED.merchant_name,
				amount = EXCLUDED.amount, transaction_date = EXCLUDED.transaction_date, pending = EXCLUDED.pending,
				category_primary = EXCLUDED.category_primary, category_detailed = EXCLUDED.category_detailed`,
			item.id, demoItemID, item.accountID, item.name, item.merchant, item.amount,
			date, item.pending, item.primary, item.detailed); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}
