package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type config struct {
	addr          string
	databaseURL   string
	appPassword   string
	sessionSecret []byte
	encryptionKey []byte
	plaidEnv      string
	plaidClientID string
	plaidSecret   string
	syncInterval  time.Duration
}

type app struct {
	cfg       config
	db        *pgxpool.Pool
	plaid     *plaidClient
	sessionID string
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

	a := &app{
		cfg:       cfg,
		db:        db,
		plaid:     newPlaidClient(cfg),
		sessionID: base64.RawURLEncoding.EncodeToString(cfg.sessionSecret[:16]),
	}
	go a.runSyncWorker(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", a.health)
	mux.HandleFunc("GET /api/auth/session", a.session)
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.Handle("POST /api/plaid/link-token", a.requireAuth(http.HandlerFunc(a.createLinkToken)))
	mux.Handle("POST /api/plaid/exchange", a.requireAuth(http.HandlerFunc(a.exchangePublicToken)))
	mux.Handle("POST /api/plaid/sync", a.requireAuth(http.HandlerFunc(a.syncNow)))
	mux.Handle("GET /api/accounts", a.requireAuth(http.HandlerFunc(a.listAccounts)))
	mux.Handle("GET /api/transactions", a.requireAuth(http.HandlerFunc(a.listTransactions)))

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
	cfg.addr = env("HTTP_ADDR", ":8080")
	cfg.databaseURL = os.Getenv("DATABASE_URL")
	cfg.appPassword = os.Getenv("APP_PASSWORD")
	cfg.plaidEnv = env("PLAID_ENV", "sandbox")
	cfg.plaidClientID = os.Getenv("PLAID_CLIENT_ID")
	cfg.plaidSecret = os.Getenv("PLAID_SECRET")
	cfg.syncInterval = 6 * time.Hour

	if cfg.appPassword == "" {
		return cfg, errors.New("APP_PASSWORD is required")
	}
	if len(cfg.appPassword) < 12 {
		return cfg, errors.New("APP_PASSWORD must be at least 12 characters")
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

func (a *app) session(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": a.isAuthenticated(r)})
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	expected := sha256.Sum256([]byte(a.cfg.appPassword))
	provided := sha256.Sum256([]byte(input.Password))
	if subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid password")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "snappy_session",
		Value:    a.sessionCookie(),
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: "snappy_session", Value: "", Path: "/", HttpOnly: true,
		Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (a *app) sessionCookie() string {
	expiry := strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)
	mac := hmac.New(sha256.New, a.cfg.sessionSecret)
	_, _ = mac.Write([]byte(expiry + ":" + a.sessionID))
	return expiry + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *app) isAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie("snappy_session")
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > expiry {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, a.cfg.sessionSecret)
	_, _ = mac.Write([]byte(parts[0] + ":" + a.sessionID))
	return hmac.Equal(signature, mac.Sum(nil))
}

func (a *app) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.isAuthenticated(r) {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
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

func (a *app) listAccounts(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `
		SELECT account_id, name, official_name, mask, type, subtype, current_balance, iso_currency_code
		FROM accounts ORDER BY name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load accounts")
		return
	}
	defer rows.Close()
	accounts := make([]account, 0)
	for rows.Next() {
		var item account
		if err := rows.Scan(&item.AccountID, &item.Name, &item.OfficialName, &item.Mask, &item.Type, &item.Subtype, &item.CurrentBalance, &item.Currency); err != nil {
			writeError(w, http.StatusInternalServerError, "could not load accounts")
			return
		}
		accounts = append(accounts, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not load accounts")
		return
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (a *app) listTransactions(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `
		SELECT transaction_id, account_id, name, merchant_name, amount, transaction_date,
		       pending, category_primary, category_detailed, iso_currency_code
		FROM transactions
		ORDER BY transaction_date DESC, transaction_id
		LIMIT 500`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load transactions")
		return
	}
	defer rows.Close()
	transactions := make([]transaction, 0)
	for rows.Next() {
		var item transaction
		if err := rows.Scan(&item.TransactionID, &item.AccountID, &item.Name, &item.MerchantName, &item.Amount,
			&item.Date, &item.Pending, &item.CategoryPrimary, &item.CategoryDetailed, &item.Currency); err != nil {
			writeError(w, http.StatusInternalServerError, "could not load transactions")
			return
		}
		transactions = append(transactions, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not load transactions")
		return
	}
	writeJSON(w, http.StatusOK, transactions)
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
	rows, err := a.db.Query(ctx, "SELECT item_id FROM plaid_items ORDER BY item_id")
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
	return nil
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
				transaction_date, pending, category_primary, category_detailed, iso_currency_code
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (transaction_id) DO UPDATE SET
				account_id=EXCLUDED.account_id, name=EXCLUDED.name, merchant_name=EXCLUDED.merchant_name,
				amount=EXCLUDED.amount, transaction_date=EXCLUDED.transaction_date, pending=EXCLUDED.pending,
				category_primary=EXCLUDED.category_primary, category_detailed=EXCLUDED.category_detailed,
				iso_currency_code=EXCLUDED.iso_currency_code, updated_at=NOW()`,
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
			if err := a.syncAll(ctx); err != nil {
				log.Printf("scheduled Plaid sync: %v", err)
			}
		}
	}
}

type account struct {
	AccountID      string   `json:"account_id"`
	Name           string   `json:"name"`
	OfficialName   *string  `json:"official_name"`
	Mask           *string  `json:"mask"`
	Type           string   `json:"type"`
	Subtype        string   `json:"subtype"`
	CurrentBalance *float64 `json:"current_balance"`
	Currency       *string  `json:"iso_currency_code"`
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
