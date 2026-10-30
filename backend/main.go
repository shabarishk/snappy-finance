package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"snappy-finance/backend/webassets"
)

type config struct {
	addr          string
	databaseURL   string
	adminUsername string
	adminPassword string
	sessionSecret []byte
	encryptionKey []byte
	plaidEnv      string
	plaidClientID string
	plaidSecret   string
	syncInterval  time.Duration
	demoData      bool
}

type app struct {
	cfg    config
	db     *pgxpool.Pool
	plaid  *plaidClient
	syncMu sync.Mutex
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	db, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		log.Fatalf("connect to PostgreSQL: %v", err)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		log.Fatalf("ping PostgreSQL: %v", err)
	}
	if err := initializeSchema(ctx, db); err != nil {
		log.Fatalf("initialize database schema: %v", err)
	}
	if err := bootstrapAdmin(ctx, db, cfg.adminUsername, cfg.adminPassword); err != nil {
		log.Fatalf("initialize administrator account: %v", err)
	}
	if cfg.demoData {
		if err := seedDemoData(ctx, db, cfg.encryptionKey); err != nil {
			log.Fatalf("seed demo data: %v", err)
		}
	}

	a := &app{
		cfg:   cfg,
		db:    db,
		plaid: newPlaidClient(cfg),
	}
	if err := a.matchInterAccountTransfers(ctx); err != nil {
		log.Fatalf("match existing inter-account transfers: %v", err)
	}
	go a.runSyncWorker(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", a.health)
	mux.HandleFunc("GET /api/auth/session", a.session)
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.Handle("GET /api/me/preferences", a.requireAuth(http.HandlerFunc(a.getPreferences)))
	mux.Handle("PUT /api/me/preferences", a.requireAuth(http.HandlerFunc(a.updatePreferences)))
	mux.Handle("GET /api/dashboard/metrics", a.requireAuth(http.HandlerFunc(a.getDashboardMetrics)))
	mux.Handle("GET /api/dashboard/liquidity", a.requireAuth(http.HandlerFunc(a.getDashboardLiquidity)))
	mux.Handle("GET /api/dashboard/liquidity/transactions", a.requireAuth(http.HandlerFunc(a.getDashboardLiquidityTransactions)))
	mux.Handle("GET /api/dashboard/subscription-tracker", a.requireAuth(http.HandlerFunc(a.getSubscriptionTracker)))
	mux.Handle("PATCH /api/admin/subscription-increases/{reviewID}", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.resolveSubscriptionIncrease))))
	mux.Handle("GET /api/projections", a.requireAuth(http.HandlerFunc(a.getProjections)))
	mux.Handle("PUT /api/admin/projections/settings", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.saveProjectionSettings))))
	mux.Handle("PUT /api/admin/projections/expenses", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.saveScheduledProjectionExpenses))))
	mux.Handle("GET /api/admin/finance-settings", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.getFinanceSettings))))
	mux.Handle("PUT /api/admin/finance-settings/expendable-account", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.setExpendableCashAccount))))
	mux.Handle("PUT /api/admin/finance-settings/credit-card-cycle", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.saveCreditCardCycle))))
	mux.Handle("PUT /api/admin/finance-settings/credit-card-statement", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.saveCreditCardStatement))))
	mux.Handle("DELETE /api/admin/finance-settings/credit-card-statement", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.deleteCreditCardStatement))))
	mux.Handle("POST /api/me/password", a.requireAuth(http.HandlerFunc(a.changePassword)))
	mux.Handle("GET /api/admin/users", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.listUsers))))
	mux.Handle("POST /api/admin/users", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.createUser))))
	mux.Handle("POST /api/plaid/link-token", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.createLinkToken))))
	mux.Handle("POST /api/plaid/exchange", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.exchangePublicToken))))
	mux.Handle("POST /api/plaid/sync", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.syncNow))))
	mux.Handle("POST /api/plaid/accounts/{accountID}/sync", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.syncAccount))))
	mux.Handle("DELETE /api/plaid/accounts/{accountID}", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.unlinkAccount))))
	mux.Handle("GET /api/accounts", a.requireAuth(http.HandlerFunc(a.listAccounts)))
	mux.Handle("GET /api/transactions", a.requireAuth(http.HandlerFunc(a.listTransactions)))
	mux.Handle("PATCH /api/admin/transactions/visibility", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.updateTransactionVisibility))))
	mux.Handle("PATCH /api/admin/transactions/category", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.updateTransactionCategory))))
	mux.Handle("GET /api/transaction-categories", a.requireAuth(http.HandlerFunc(a.listTransactionCategories)))
	mux.Handle("PATCH /api/admin/transaction-categories/{categoryID}", a.requireAuth(a.requireAdmin(http.HandlerFunc(a.updateTransactionCategoryExclusion))))
	mux.Handle("/", webassets.Handler())

	server := &http.Server{
		Addr:              cfg.addr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("API listening on %s", cfg.addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func loadConfig() (config, error) {
	var cfg config
	var err error
	cfg.addr = env("HTTP_ADDR", ":8080")
	cfg.databaseURL = os.Getenv("DATABASE_URL")
	cfg.adminUsername = env("ADMIN_USERNAME", "admin")
	cfg.adminPassword = os.Getenv("ADMIN_PASSWORD")
	if cfg.adminPassword == "" {
		cfg.adminPassword = os.Getenv("APP_PASSWORD")
	}
	cfg.plaidEnv = env("PLAID_ENV", "sandbox")
	cfg.plaidClientID = os.Getenv("PLAID_CLIENT_ID")
	cfg.plaidSecret = os.Getenv("PLAID_SECRET")
	cfg.syncInterval = 6 * time.Hour
	if value := os.Getenv("DEMO_DATA"); value != "" {
		cfg.demoData, err = strconv.ParseBool(value)
		if err != nil {
			return cfg, errors.New("DEMO_DATA must be true or false")
		}
	}

	if cfg.adminUsername, err = normalizeUsername(cfg.adminUsername); err != nil {
		return cfg, fmt.Errorf("ADMIN_USERNAME: %w", err)
	}
	if cfg.adminPassword != "" {
		if err := validatePassword(cfg.adminPassword); err != nil {
			return cfg, fmt.Errorf("ADMIN_PASSWORD: %w", err)
		}
	}
	sessionSecret := os.Getenv("SESSION_SECRET")
	if len(sessionSecret) < 32 {
		return cfg, errors.New("SESSION_SECRET must contain at least 32 characters")
	}
	cfg.sessionSecret = []byte(sessionSecret)
	encryptionKey, err := base64.StdEncoding.DecodeString(os.Getenv("APP_ENCRYPTION_KEY"))
	if err != nil || len(encryptionKey) != 32 {
		return cfg, errors.New("APP_ENCRYPTION_KEY must be a base64-encoded 32-byte key")
	}
	cfg.encryptionKey = encryptionKey
	if cfg.plaidEnv != "sandbox" && cfg.plaidEnv != "development" && cfg.plaidEnv != "production" {
		return cfg, errors.New("PLAID_ENV must be sandbox, development, or production")
	}
	if interval := os.Getenv("SYNC_INTERVAL"); interval != "" {
		cfg.syncInterval, err = time.ParseDuration(interval)
		if err != nil || cfg.syncInterval < time.Minute {
			return cfg, errors.New("SYNC_INTERVAL must be a duration of at least 1 minute")
		}
	}
	return cfg, nil
}

func initializeSchema(ctx context.Context, db *pgxpool.Pool) error {
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("read schema.sql: %w", err)
	}
	_, err = db.Exec(ctx, string(schema))
	return err
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func (a *app) health(w http.ResponseWriter, r *http.Request) {
	if err := a.db.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) createLinkToken(w http.ResponseWriter, r *http.Request) {
	if a.plaid.configured() == false {
		writeError(w, http.StatusServiceUnavailable, "Plaid credentials are not configured")
		return
	}
	token, err := a.plaid.createLinkToken(r.Context())
	if err != nil {
		log.Printf("create Plaid Link token: %v", err)
		writeError(w, http.StatusBadGateway, "could not create Plaid Link token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"link_token": token})
}

func (a *app) exchangePublicToken(w http.ResponseWriter, r *http.Request) {
	var input struct {
		PublicToken string `json:"public_token"`
	}
	if err := decodeJSON(r, &input); err != nil || input.PublicToken == "" {
		writeError(w, http.StatusBadRequest, "public_token is required")
		return
	}
	exchange, err := a.plaid.exchangePublicToken(r.Context(), input.PublicToken)
	if err != nil {
		log.Printf("exchange Plaid public token: %v", err)
		writeError(w, http.StatusBadGateway, "could not connect Plaid Item")
		return
	}
	encrypted, err := encrypt(a.cfg.encryptionKey, exchange.AccessToken)
	if err != nil {
		log.Printf("encrypt Plaid access token: %v", err)
		writeError(w, http.StatusInternalServerError, "could not securely store Plaid connection")
		return
	}
	_, err = a.db.Exec(r.Context(), `
		INSERT INTO plaid_items (item_id, access_token_encrypted)
		VALUES ($1, $2)
		ON CONFLICT (item_id) DO NOTHING`, exchange.ItemID, encrypted)
	if err != nil {
		log.Printf("store Plaid Item: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save Plaid connection")
		return
	}
	if err := a.refreshAccounts(r.Context(), exchange.ItemID, exchange.AccessToken); err != nil {
		log.Printf("fetch Plaid accounts: %v", err)
	}
	if err := a.syncItem(r.Context(), exchange.ItemID); err != nil {
		log.Printf("initial transaction sync: %v", err)
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"connected": true})
}

func (a *app) syncNow(w http.ResponseWriter, r *http.Request) {
	if err := a.syncAll(r.Context()); err != nil {
		log.Printf("manual Plaid sync: %v", err)
		writeError(w, http.StatusBadGateway, "sync failed; check the API logs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"synced": true})
}

func (a *app) syncAccount(w http.ResponseWriter, r *http.Request) {
	accountID := r.PathValue("accountID")
	var itemID string
	var demoData bool
	err := a.db.QueryRow(r.Context(), `
		SELECT accounts.item_id, plaid_items.demo_data
		FROM accounts
		JOIN plaid_items USING (item_id)
		WHERE accounts.account_id = $1`, accountID).Scan(&itemID, &demoData)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	if err != nil {
		log.Printf("load account connection for sync: %v", err)
		writeError(w, http.StatusInternalServerError, "could not sync account")
		return
	}
	if demoData {
		writeError(w, http.StatusBadRequest, "demo accounts cannot be synced")
		return
	}
	if err := a.syncItem(r.Context(), itemID); err != nil {
		log.Printf("sync Plaid account %s: %v", accountID, err)
		writeError(w, http.StatusBadGateway, "account connection sync failed; check the API logs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"synced": true})
}

func (a *app) unlinkAccount(w http.ResponseWriter, r *http.Request) {
	if !a.plaid.configured() {
		writeError(w, http.StatusServiceUnavailable, "Plaid credentials are not configured")
		return
	}

	a.syncMu.Lock()
	defer a.syncMu.Unlock()

	var itemID string
	var encrypted []byte
	var demoData bool
	var accountCount int
	err := a.db.QueryRow(r.Context(), `
		SELECT item.item_id, item.access_token_encrypted, item.demo_data,
		       (SELECT COUNT(*) FROM accounts linked WHERE linked.item_id = item.item_id)
		FROM accounts requested
		JOIN plaid_items item USING (item_id)
		WHERE requested.account_id = $1`, r.PathValue("accountID")).Scan(&itemID, &encrypted, &demoData, &accountCount)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	if err != nil {
		log.Printf("load Plaid connection for unlink: %v", err)
		writeError(w, http.StatusInternalServerError, "could not unlink account")
		return
	}
	if demoData {
		writeError(w, http.StatusBadRequest, "demo connections cannot be unlinked")
		return
	}
	accessToken, err := decrypt(a.cfg.encryptionKey, encrypted)
	if err != nil {
		log.Printf("decrypt Plaid access token for unlink: %v", err)
		writeError(w, http.StatusInternalServerError, "could not unlink account")
		return
	}
	if err := a.plaid.removeItem(r.Context(), accessToken); err != nil {
		log.Printf("remove Plaid Item %s: %v", itemID, err)
		writeError(w, http.StatusBadGateway, "Plaid could not unlink this connection")
		return
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		log.Printf("begin Plaid unlink: %v", err)
		writeError(w, http.StatusInternalServerError, "could not unlink account")
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), "DELETE FROM plaid_items WHERE item_id = $1 AND demo_data = FALSE", itemID)
	if err != nil {
		log.Printf("delete unlinked Plaid Item: %v", err)
		writeError(w, http.StatusInternalServerError, "Plaid unlinked the connection but local account data could not be removed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "account connection was already removed")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("commit Plaid unlink: %v", err)
		writeError(w, http.StatusInternalServerError, "Plaid unlinked the connection but local account data could not be removed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unlinked": true, "accounts_removed": accountCount})
}

func (a *app) listAccounts(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `
		SELECT accounts.account_id, accounts.name, accounts.official_name, accounts.mask, accounts.type,
		       accounts.subtype, accounts.current_balance, accounts.iso_currency_code, plaid_items.demo_data,
		       cycle.statement_close_day, cycle.statement_due_day, accounts.item_id, plaid_items.last_synced_at
		FROM accounts
		JOIN plaid_items USING (item_id)
		LEFT JOIN credit_card_cycles cycle USING (account_id)
		ORDER BY accounts.name`)
	if err != nil {
		log.Printf("query accounts: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load accounts")
		return
	}
	defer rows.Close()
	accounts := make([]account, 0)
	for rows.Next() {
		var item account
		if err := rows.Scan(&item.AccountID, &item.Name, &item.OfficialName, &item.Mask, &item.Type, &item.Subtype, &item.CurrentBalance, &item.Currency, &item.DemoData, &item.StatementCloseDay, &item.StatementDueDay, &item.ItemID, &item.LastSyncedAt); err != nil {
			log.Printf("scan account row: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load accounts")
			return
		}
		accounts = append(accounts, item)
	}
	if err := rows.Err(); err != nil {
		log.Printf("iterate account rows: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load accounts")
		return
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (a *app) listTransactions(w http.ResponseWriter, r *http.Request) {
	isAdmin := currentPrincipal(r).Role == "admin"
	rows, err := a.db.Query(r.Context(), `
		SELECT transaction_id, account_id, name, merchant_name, amount, transaction_date::text,
		       pending, category_primary, category_detailed, iso_currency_code,
		       CASE WHEN $1 THEN hidden_from_users ELSE FALSE END, category_id,
		       category_match_key <> ''
		FROM transactions
		WHERE $1 OR NOT hidden_from_users
		ORDER BY transaction_date DESC, transaction_id
		LIMIT 500`, isAdmin)
	if err != nil {
		log.Printf("query transactions: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load transactions")
		return
	}
	defer rows.Close()
	transactions := make([]transaction, 0)
	for rows.Next() {
		var item transaction
		if err := rows.Scan(&item.TransactionID, &item.AccountID, &item.Name, &item.MerchantName, &item.Amount,
			&item.Date, &item.Pending, &item.CategoryPrimary, &item.CategoryDetailed, &item.Currency,
			&item.HiddenFromUsers, &item.CategoryID, &item.HasCategoryMatch); err != nil {
			log.Printf("scan transaction row: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load transactions")
			return
		}
		transactions = append(transactions, item)
	}
	if err := rows.Err(); err != nil {
		log.Printf("iterate transaction rows: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load transactions")
		return
	}
	writeJSON(w, http.StatusOK, transactions)
}

func (a *app) updateTransactionVisibility(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TransactionID   string `json:"transaction_id"`
		HiddenFromUsers *bool  `json:"hidden_from_users"`
	}
	if err := decodeJSON(r, &input); err != nil || input.TransactionID == "" || input.HiddenFromUsers == nil {
		writeError(w, http.StatusBadRequest, "transaction_id and hidden_from_users are required")
		return
	}
	tag, err := a.db.Exec(r.Context(), `
		UPDATE transactions
		SET hidden_from_users = $1
		WHERE transaction_id = $2`, *input.HiddenFromUsers, input.TransactionID)
	if err != nil {
		log.Printf("update transaction visibility: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update transaction visibility")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "transaction not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"hidden_from_users": *input.HiddenFromUsers})
}

func (a *app) listTransactionCategories(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `
		SELECT category_id, label, category_type, excluded_from_totals
		FROM transaction_categories
		ORDER BY label`)
	if err != nil {
		log.Printf("query transaction categories: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load transaction categories")
		return
	}
	defer rows.Close()
	categories := make([]transactionCategory, 0)
	for rows.Next() {
		var category transactionCategory
		if err := rows.Scan(&category.CategoryID, &category.Label, &category.CategoryType, &category.ExcludedFromTotals); err != nil {
			log.Printf("scan transaction category: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load transaction categories")
			return
		}
		categories = append(categories, category)
	}
	if err := rows.Err(); err != nil {
		log.Printf("iterate transaction categories: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load transaction categories")
		return
	}
	writeJSON(w, http.StatusOK, categories)
}

func (a *app) updateTransactionCategory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TransactionID string  `json:"transaction_id"`
		CategoryID    *string `json:"category_id"`
		Scope         string  `json:"scope"`
	}
	if err := decodeJSON(r, &input); err != nil || input.TransactionID == "" || input.CategoryID == nil {
		writeError(w, http.StatusBadRequest, "transaction_id and category_id are required")
		return
	}
	categoryID := strings.TrimSpace(*input.CategoryID)
	scope := input.Scope
	if scope == "" {
		scope = "related"
	}
	if categoryID == "" {
		scope = "single"
	}
	if scope != "single" && scope != "related" {
		writeError(w, http.StatusBadRequest, "category scope must be single or related")
		return
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		log.Printf("begin transaction category update: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update transaction category")
		return
	}
	defer tx.Rollback(r.Context())

	var matchKey string
	var isIncome bool
	err = tx.QueryRow(r.Context(), `
		SELECT category_match_key, amount < 0
		FROM transactions
		WHERE transaction_id = $1
		FOR UPDATE`, input.TransactionID).Scan(&matchKey, &isIncome)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "transaction not found")
		return
	}
	if err != nil {
		log.Printf("load transaction for category update: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update transaction category")
		return
	}

	var categoryType string
	if categoryID != "" {
		err := tx.QueryRow(r.Context(), `
			SELECT category_type
			FROM transaction_categories
			WHERE category_id = $1`, categoryID).Scan(&categoryType)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "unknown transaction category")
			return
		}
		if err != nil {
			log.Printf("load transaction category: %v", err)
			writeError(w, http.StatusInternalServerError, "could not update transaction category")
			return
		}
		if (categoryType == "income") != isIncome {
			writeError(w, http.StatusBadRequest, "category type does not match transaction amount")
			return
		}
	}

	if categoryID != "" && matchKey != "" && scope == "related" {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO transaction_category_rules (category_type, match_key, category_id)
			VALUES ($1, $2, $3)
			ON CONFLICT (category_type, match_key) DO UPDATE
			SET category_id = EXCLUDED.category_id, updated_at = NOW()`,
			categoryType, matchKey, categoryID)
		if err != nil {
			log.Printf("save transaction category rule: %v", err)
			writeError(w, http.StatusInternalServerError, "could not update transaction category")
			return
		}
	}

	var updateQuery string
	var updateArgs []any
	if categoryID == "" {
		updateQuery = `
			UPDATE transactions
			SET category_id = NULL, category_rule_exempt = TRUE
			WHERE transaction_id = $1
			RETURNING transaction_id`
		updateArgs = []any{input.TransactionID}
	} else if matchKey == "" || scope == "single" {
		updateQuery = `
			UPDATE transactions
			SET category_id = $1, category_rule_exempt = TRUE
			WHERE transaction_id = $2
			RETURNING transaction_id`
		updateArgs = []any{categoryID, input.TransactionID}
	} else {
		updateQuery = `
			UPDATE transactions
			SET category_id = $1, category_rule_exempt = FALSE
			WHERE category_match_key = $2 AND (amount < 0) = $3
			RETURNING transaction_id`
		updateArgs = []any{categoryID, matchKey, isIncome}
	}

	rows, err := tx.Query(r.Context(), updateQuery, updateArgs...)
	if err != nil {
		log.Printf("apply transaction category: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update transaction category")
		return
	}
	matchedTransactionIDs := make([]string, 0)
	for rows.Next() {
		var transactionID string
		if err := rows.Scan(&transactionID); err != nil {
			rows.Close()
			log.Printf("scan categorized transaction: %v", err)
			writeError(w, http.StatusInternalServerError, "could not update transaction category")
			return
		}
		matchedTransactionIDs = append(matchedTransactionIDs, transactionID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		log.Printf("iterate categorized transactions: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update transaction category")
		return
	}
	rows.Close()

	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("commit transaction category update: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update transaction category")
		return
	}
	if err := a.matchInterAccountTransfers(r.Context()); err != nil {
		log.Printf("refresh inter-account transfer matches after category update: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update transaction category")
		return
	}
	var result *string
	if categoryID != "" {
		result = &categoryID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"category_id":             result,
		"matched_transaction_ids": matchedTransactionIDs,
		"matching_rule_applied":   categoryID != "" && matchKey != "" && scope == "related",
	})
}

func (a *app) updateTransactionCategoryExclusion(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ExcludedFromTotals *bool `json:"excluded_from_totals"`
	}
	categoryID := r.PathValue("categoryID")
	if err := decodeJSON(r, &input); err != nil || categoryID == "" || input.ExcludedFromTotals == nil {
		writeError(w, http.StatusBadRequest, "excluded_from_totals is required")
		return
	}
	tag, err := a.db.Exec(r.Context(), `
		UPDATE transaction_categories
		SET excluded_from_totals = $1
		WHERE category_id = $2`, *input.ExcludedFromTotals, categoryID)
	if err != nil {
		log.Printf("update transaction category exclusion: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update transaction category")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "transaction category not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"excluded_from_totals": *input.ExcludedFromTotals})
}

func (a *app) refreshAccounts(ctx context.Context, itemID, accessToken string) error {
	result, err := a.plaid.accountsGet(ctx, accessToken)
	if err != nil {
		return err
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, item := range result.Accounts {
		_, err := tx.Exec(ctx, `
			INSERT INTO accounts (account_id, item_id, name, official_name, mask, type, subtype, current_balance, iso_currency_code)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (account_id) DO UPDATE SET
				name=EXCLUDED.name, official_name=EXCLUDED.official_name, mask=EXCLUDED.mask,
				type=EXCLUDED.type, subtype=EXCLUDED.subtype, current_balance=EXCLUDED.current_balance,
				iso_currency_code=EXCLUDED.iso_currency_code, updated_at=NOW()`,
			item.AccountID, itemID, item.Name, item.OfficialName, item.Mask, item.Type, item.Subtype,
			item.Balances.Current, item.Balances.IsoCurrencyCode)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (a *app) syncAll(ctx context.Context) error {
	rows, err := a.db.Query(ctx, "SELECT item_id FROM plaid_items WHERE demo_data = FALSE ORDER BY item_id")
	if err != nil {
		return err
	}
	var itemIDs []string
	for rows.Next() {
		var itemID string
		if err := rows.Scan(&itemID); err != nil {
			rows.Close()
			return err
		}
		itemIDs = append(itemIDs, itemID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var firstErr error
	for _, itemID := range itemIDs {
		if err := a.syncItem(ctx, itemID); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("sync Item %s: %w", itemID, err)
		}
	}
	return firstErr
}

func (a *app) syncItem(ctx context.Context, itemID string) error {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()

	var encrypted []byte
	var cursor string
	err := a.db.QueryRow(ctx, "SELECT access_token_encrypted, cursor FROM plaid_items WHERE item_id=$1", itemID).Scan(&encrypted, &cursor)
	if err != nil {
		return err
	}
	accessToken, err := decrypt(a.cfg.encryptionKey, encrypted)
	if err != nil {
		return fmt.Errorf("decrypt access token: %w", err)
	}
	nextCursor := cursor
	for {
		result, err := a.plaid.transactionsSync(ctx, accessToken, nextCursor)
		if err != nil {
			return err
		}
		if err := a.persistTransactionPage(ctx, itemID, result); err != nil {
			return err
		}
		nextCursor = result.NextCursor
		if !result.HasMore {
			break
		}
	}
	_, err = a.db.Exec(ctx, "UPDATE plaid_items SET cursor=$2, last_synced_at=NOW() WHERE item_id=$1", itemID, nextCursor)
	if err != nil {
		return err
	}
	liabilities, err := a.plaid.liabilitiesGet(ctx, accessToken)
	if err != nil {
		log.Printf("Plaid credit statement data unavailable for Item %s (manual entry remains available): %v", itemID, err)
	} else if err := a.persistCreditStatements(ctx, liabilities); err != nil {
		return fmt.Errorf("persist Plaid credit statements: %w", err)
	}
	if err := a.matchInterAccountTransfers(ctx); err != nil {
		return fmt.Errorf("match inter-account transfers: %w", err)
	}
	return nil
}

func (a *app) persistCreditStatements(ctx context.Context, liabilities []plaidCreditLiability) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, liability := range liabilities {
		if liability.LastStatementBalance == nil || liability.NextPaymentDueDate == nil {
			continue
		}
		dueDate, err := time.Parse("2006-01-02", *liability.NextPaymentDueDate)
		if err != nil {
			return fmt.Errorf("parse Plaid payment due date: %w", err)
		}
		var statementDate any
		if liability.LastStatementIssueDate != nil {
			parsedDate, err := time.Parse("2006-01-02", *liability.LastStatementIssueDate)
			if err != nil {
				return fmt.Errorf("parse Plaid statement issue date: %w", err)
			}
			statementDate = parsedDate
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO credit_card_statements (
				account_id, due_date, statement_date, statement_balance, source
			)
			SELECT $1, $2, $3, $4, 'plaid'
			WHERE EXISTS (
				SELECT 1 FROM accounts WHERE account_id = $1 AND type = 'credit'
			)
			ON CONFLICT (account_id, due_date, source) DO UPDATE SET
				statement_date = EXCLUDED.statement_date,
				statement_balance = EXCLUDED.statement_balance,
				updated_at = NOW()`,
			liability.AccountID, dueDate, statementDate, *liability.LastStatementBalance)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (a *app) persistTransactionPage(ctx context.Context, itemID string, result syncResponse) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, item := range append(result.Added, result.Modified...) {
		_, err := tx.Exec(ctx, `
			INSERT INTO transactions (
				transaction_id, item_id, account_id, name, merchant_name, amount,
				transaction_date, pending, category_primary, category_detailed, iso_currency_code,
				category_match_key, category_id
			) VALUES (
				$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,
				transaction_category_match_key($5, $4, $3, ($6::numeric < 0::numeric)),
				COALESCE(
					(SELECT category_id
					 FROM transaction_category_rules
					 WHERE match_key = transaction_category_match_key($5, $4, $3, ($6::numeric < 0::numeric))
					   AND category_type = CASE WHEN $6::numeric < 0::numeric THEN 'income' ELSE 'expense' END),
					transaction_bank_category_mapping($10, $6)
				)
			)
			ON CONFLICT (transaction_id) DO UPDATE SET
				account_id=EXCLUDED.account_id, name=EXCLUDED.name, merchant_name=EXCLUDED.merchant_name,
				amount=EXCLUDED.amount, transaction_date=EXCLUDED.transaction_date, pending=EXCLUDED.pending,
				category_primary=EXCLUDED.category_primary, category_detailed=EXCLUDED.category_detailed,
				iso_currency_code=EXCLUDED.iso_currency_code, category_match_key=EXCLUDED.category_match_key,
				category_id=CASE WHEN transactions.category_rule_exempt THEN transactions.category_id ELSE EXCLUDED.category_id END,
				updated_at=NOW()`,
			item.TransactionID, itemID, item.AccountID, item.Name, item.MerchantName, item.Amount,
			item.Date, item.Pending, item.PersonalFinanceCategory.Primary,
			item.PersonalFinanceCategory.Detailed, item.IsoCurrencyCode)
		if err != nil {
			return err
		}
	}
	for _, removed := range result.Removed {
		if _, err := tx.Exec(ctx, "DELETE FROM transactions WHERE transaction_id=$1 AND item_id=$2", removed.TransactionID, itemID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (a *app) runSyncWorker(ctx context.Context) {
	if _, _, _, err := a.refreshOpenIncomeProjections(ctx); err != nil {
		log.Printf("refresh income projections at startup: %v", err)
	}
	if err := a.syncAll(ctx); err != nil {
		log.Printf("startup Plaid sync: %v", err)
	}
	ticker := time.NewTicker(a.cfg.syncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, _, _, err := a.refreshOpenIncomeProjections(ctx); err != nil {
				log.Printf("refresh scheduled income projections: %v", err)
			}
			if err := a.syncAll(ctx); err != nil {
				log.Printf("scheduled Plaid sync: %v", err)
			}
		}
	}
}

type account struct {
	AccountID         string     `json:"account_id"`
	ItemID            string     `json:"item_id"`
	Name              string     `json:"name"`
	OfficialName      *string    `json:"official_name"`
	Mask              *string    `json:"mask"`
	Type              string     `json:"type"`
	Subtype           string     `json:"subtype"`
	CurrentBalance    *float64   `json:"current_balance"`
	Currency          *string    `json:"iso_currency_code"`
	DemoData          bool       `json:"demo_data"`
	StatementCloseDay *int16     `json:"statement_close_day"`
	StatementDueDay   *int16     `json:"statement_due_day"`
	LastSyncedAt      *time.Time `json:"last_synced_at"`
}

type transaction struct {
	TransactionID    string  `json:"transaction_id"`
	AccountID        string  `json:"account_id"`
	Name             string  `json:"name"`
	MerchantName     *string `json:"merchant_name"`
	Amount           float64 `json:"amount"`
	Date             string  `json:"date"`
	Pending          bool    `json:"pending"`
	CategoryPrimary  *string `json:"category_primary"`
	CategoryDetailed *string `json:"category_detailed"`
	Currency         *string `json:"iso_currency_code"`
	HiddenFromUsers  bool    `json:"hidden_from_users,omitempty"`
	CategoryID       *string `json:"category_id"`
	HasCategoryMatch bool    `json:"has_category_match"`
}

type transactionCategory struct {
	CategoryID         string `json:"category_id"`
	Label              string `json:"label"`
	CategoryType       string `json:"category_type"`
	ExcludedFromTotals bool   `json:"excluded_from_totals"`
}

func encrypt(key []byte, plaintext string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func decrypt(key, ciphertext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", errors.New("encrypted value is too short")
	}
	nonce, encrypted := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, encrypted, nil)
	return string(plaintext), err
}

func decodeJSON(r *http.Request, destination any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
