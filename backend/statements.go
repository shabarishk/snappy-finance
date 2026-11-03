package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxStatementUploadSize = 20 << 20
	statementParserTimeout = 75 * time.Second
)

type statementParser struct {
	ParserID    string `json:"parser_id"`
	DisplayName string `json:"display_name"`
}

var bundledStatementParsers = []statementParser{
	{ParserID: "bilt", DisplayName: "Bilt card statement"},
}

type parsedStatement struct {
	PeriodStart  *string                      `json:"period_start"`
	PeriodEnd    *string                      `json:"period_end"`
	Transactions []parsedStatementTransaction `json:"transactions"`
}

type parsedStatementTransaction struct {
	Date     string  `json:"date"`
	Merchant string  `json:"merchant"`
	Amount   float64 `json:"amount"`
}

type plaidStatementTransaction struct {
	TransactionID string
	Date          string
	Merchant      string
	Amount        float64
}

type statementMatch struct {
	Status string
	Plaid  *plaidStatementTransaction
}

type importedStatementSummary struct {
	StatementID         string    `json:"statement_id"`
	AccountID           string    `json:"account_id"`
	AccountName         string    `json:"account_name"`
	ParserID            string    `json:"parser_id"`
	ReviewStatus        string    `json:"review_status"`
	PeriodStart         string    `json:"period_start"`
	PeriodEnd           string    `json:"period_end"`
	ImportedAt          time.Time `json:"imported_at"`
	TransactionCount    int       `json:"transaction_count"`
	MatchedCount        int       `json:"matched_count"`
	StatementOnlyCount  int       `json:"statement_only_count"`
	AmbiguousCount      int       `json:"ambiguous_count"`
	PlaidOnlyCount      int       `json:"plaid_only_count"`
	AmbiguousPlaidCount int       `json:"ambiguous_plaid_count"`
}

type statementImportTransaction struct {
	TransactionID             int64    `json:"statement_transaction_id"`
	Date                      string   `json:"date"`
	Merchant                  string   `json:"merchant"`
	Amount                    float64  `json:"amount"`
	MatchStatus               string   `json:"match_status"`
	MatchedPlaidTransactionID *string  `json:"matched_plaid_transaction_id"`
	MatchedPlaidDate          *string  `json:"matched_plaid_date"`
	MatchedPlaidMerchant      *string  `json:"matched_plaid_merchant"`
	MatchedPlaidAmount        *float64 `json:"matched_plaid_amount"`
}

type statementDiscrepancy struct {
	DiscrepancyID int64   `json:"discrepancy_id"`
	TransactionID string  `json:"transaction_id"`
	Date          string  `json:"date"`
	Merchant      string  `json:"merchant"`
	Amount        float64 `json:"amount"`
	IssueType     string  `json:"issue_type"`
}

func (a *app) listStatementParsers(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `
		SELECT parser_id, display_name
		FROM statement_parsers
		WHERE enabled
		ORDER BY display_name`)
	if err != nil {
		log.Printf("query statement parsers: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load statement parsers")
		return
	}
	defer rows.Close()
	parsers := make([]statementParser, 0)
	for rows.Next() {
		var parser statementParser
		if err := rows.Scan(&parser.ParserID, &parser.DisplayName); err != nil {
			log.Printf("scan statement parser: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load statement parsers")
			return
		}
		parsers = append(parsers, parser)
	}
	if err := rows.Err(); err != nil {
		log.Printf("iterate statement parsers: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load statement parsers")
		return
	}
	writeJSON(w, http.StatusOK, parsers)
}

func (a *app) setAccountStatementParser(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ParserID string `json:"parser_id"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid statement parser selection")
		return
	}
	if input.ParserID != "" {
		var enabled bool
		err := a.db.QueryRow(r.Context(),
			"SELECT enabled FROM statement_parsers WHERE parser_id=$1",
			input.ParserID,
		).Scan(&enabled)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !enabled) {
			writeError(w, http.StatusBadRequest, "unknown or disabled statement parser")
			return
		}
		if err != nil {
			log.Printf("validate statement parser: %v", err)
			writeError(w, http.StatusInternalServerError, "could not save statement parser")
			return
		}
	}
	tag, err := a.db.Exec(r.Context(),
		"UPDATE accounts SET statement_parser_id=NULLIF($1, '') WHERE account_id=$2",
		input.ParserID, r.PathValue("accountID"))
	if err != nil {
		log.Printf("save account statement parser: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save statement parser")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"parser_id": input.ParserID})
}

func (a *app) uploadAccountStatement(w http.ResponseWriter, r *http.Request) {
	var accountName string
	var parserID *string
	err := a.db.QueryRow(r.Context(), `
		SELECT COALESCE(NULLIF(official_name, ''), name), statement_parser_id
		FROM accounts
		WHERE account_id=$1`,
		r.PathValue("accountID"),
	).Scan(&accountName, &parserID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	if err != nil {
		log.Printf("load account for statement import: %v", err)
		writeError(w, http.StatusInternalServerError, "could not prepare statement import")
		return
	}
	if parserID == nil || *parserID == "" {
		writeError(w, http.StatusConflict, "select a statement parser for this account before uploading")
		return
	}
	if !isBundledStatementParser(*parserID) {
		writeError(w, http.StatusConflict, "the selected statement parser is not available in this app version")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxStatementUploadSize+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "statement upload exceeds the 20 MB limit")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid statement upload")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("statement")
	if err != nil {
		writeError(w, http.StatusBadRequest, "a PDF statement file is required")
		return
	}
	defer file.Close()
	if header.Size > maxStatementUploadSize {
		writeError(w, http.StatusRequestEntityTooLarge, "statement upload exceeds the 20 MB limit")
		return
	}
	signature := make([]byte, 5)
	if _, err := io.ReadFull(file, signature); err != nil || string(signature) != "%PDF-" {
		writeError(w, http.StatusBadRequest, "uploaded file is not a valid PDF")
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusBadRequest, "could not read uploaded statement")
		return
	}

	temporaryDirectory, err := os.MkdirTemp("", "snappy-statement-")
	if err != nil {
		log.Printf("create statement parser workspace: %v", err)
		writeError(w, http.StatusInternalServerError, "could not prepare statement parser")
		return
	}
	defer os.RemoveAll(temporaryDirectory)
	pdfPath := filepath.Join(temporaryDirectory, "statement.pdf")
	pdfFile, err := os.OpenFile(pdfPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("create temporary statement PDF: %v", err)
		writeError(w, http.StatusInternalServerError, "could not prepare statement parser")
		return
	}
	hasher := sha256.New()
	copied, copyErr := io.Copy(io.MultiWriter(pdfFile, hasher), io.LimitReader(file, maxStatementUploadSize+1))
	closeErr := pdfFile.Close()
	if copyErr != nil || closeErr != nil {
		log.Printf("write temporary statement PDF: copy=%v close=%v", copyErr, closeErr)
		writeError(w, http.StatusInternalServerError, "could not read uploaded statement")
		return
	}
	if copied > maxStatementUploadSize {
		writeError(w, http.StatusRequestEntityTooLarge, "statement upload exceeds the 20 MB limit")
		return
	}
	sourceHash := hex.EncodeToString(hasher.Sum(nil))

	jsonPath := filepath.Join(temporaryDirectory, "statement.json")
	spreadsheetPath := filepath.Join(temporaryDirectory, "statement.xlsx")
	commandArgs := []string{
		a.cfg.statementParserScript,
		"--input", pdfPath,
		"--json-output", jsonPath,
		"--spreadsheet-output", spreadsheetPath,
	}
	periodStartOverride := r.FormValue("period_start")
	periodEndOverride := r.FormValue("period_end")
	if periodStartOverride != "" || periodEndOverride != "" {
		if _, _, err := validateStatementPeriod(periodStartOverride, periodEndOverride); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		commandArgs = append(commandArgs, "--period-start", periodStartOverride, "--period-end", periodEndOverride)
	}
	parserContext, cancel := context.WithTimeout(r.Context(), statementParserTimeout)
	defer cancel()
	command := exec.CommandContext(parserContext, "python3", commandArgs...)
	parserOutput, err := command.CombinedOutput()
	if err != nil {
		if errors.Is(parserContext.Err(), context.DeadlineExceeded) {
			writeError(w, http.StatusGatewayTimeout, "statement parsing exceeded the time limit")
			return
		}
		log.Printf("parse statement for account %s: %v: %s", r.PathValue("accountID"), err, strings.TrimSpace(string(parserOutput)))
		message := strings.TrimSpace(string(parserOutput))
		if message == "" {
			message = "could not parse the statement PDF"
		}
		writeError(w, http.StatusUnprocessableEntity, message)
		return
	}

	parsedBytes, err := os.ReadFile(jsonPath)
	if err != nil {
		log.Printf("read parsed statement output: %v", err)
		writeError(w, http.StatusInternalServerError, "statement parser did not produce transaction data")
		return
	}
	var parsed parsedStatement
	if err := json.Unmarshal(parsedBytes, &parsed); err != nil {
		log.Printf("decode parsed statement output: %v", err)
		writeError(w, http.StatusInternalServerError, "statement parser produced invalid transaction data")
		return
	}
	periodStart, periodEnd, err := resolveStatementPeriod(parsed, periodStartOverride, periodEndOverride)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if len(parsed.Transactions) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "no transactions were found in the statement period")
		return
	}
	for _, row := range parsed.Transactions {
		if _, err := time.Parse("2006-01-02", row.Date); err != nil || strings.TrimSpace(row.Merchant) == "" ||
			math.IsNaN(row.Amount) || math.IsInf(row.Amount, 0) {
			writeError(w, http.StatusUnprocessableEntity, "statement parser returned an invalid transaction row")
			return
		}
		if row.Date < periodStart || row.Date > periodEnd {
			writeError(w, http.StatusUnprocessableEntity, "parsed transactions fall outside the selected statement period; verify the period dates")
			return
		}
	}
	spreadsheet, err := os.ReadFile(spreadsheetPath)
	if err != nil {
		log.Printf("read generated statement spreadsheet: %v", err)
		writeError(w, http.StatusInternalServerError, "statement parser did not produce a spreadsheet")
		return
	}
	candidates, err := a.loadPlaidStatementTransactions(r.Context(), r.PathValue("accountID"), periodStart, periodEnd)
	if err != nil {
		log.Printf("load Plaid transactions for statement reconciliation: %v", err)
		writeError(w, http.StatusInternalServerError, "could not reconcile statement transactions")
		return
	}
	matches, ambiguousPlaidIDs := reconcileStatementRows(parsed.Transactions, candidates)
	periodStartDate, _ := time.Parse("2006-01-02", periodStart)
	periodEndDate, _ := time.Parse("2006-01-02", periodEnd)
	matchedPlaidIDs := make(map[string]bool)
	for _, match := range matches {
		if match.Plaid != nil {
			matchedPlaidIDs[match.Plaid.TransactionID] = true
		}
	}

	statementID, err := newStatementID()
	if err != nil {
		log.Printf("generate statement ID: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save statement import")
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		log.Printf("begin statement import transaction: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save statement import")
		return
	}
	defer tx.Rollback(r.Context())
	err = tx.QueryRow(r.Context(), `
		INSERT INTO imported_statements (
			statement_id, account_id, account_name, parser_id, period_start, period_end,
			source_sha256, spreadsheet, review_status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending_review')
		ON CONFLICT (account_id, source_sha256) DO NOTHING
		RETURNING statement_id`,
		statementID, r.PathValue("accountID"), accountName, *parserID, periodStart, periodEnd, sourceHash, spreadsheet,
	).Scan(&statementID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "this PDF has already been imported for this account")
		return
	}
	if err != nil {
		log.Printf("insert imported statement: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save statement import")
		return
	}
	for index, row := range parsed.Transactions {
		match := matches[index]
		var matchedID, matchedDate, matchedMerchant any
		var matchedAmount any
		if match.Plaid != nil {
			matchedID = match.Plaid.TransactionID
			matchedDate = match.Plaid.Date
			matchedMerchant = match.Plaid.Merchant
			matchedAmount = match.Plaid.Amount
		}
		_, err := tx.Exec(r.Context(), `
			INSERT INTO statement_import_transactions (
				statement_id, row_number, transaction_date, merchant, amount, match_status,
				matched_plaid_transaction_id, matched_plaid_date, matched_plaid_merchant,
				matched_plaid_amount
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			statementID, index+1, row.Date, strings.TrimSpace(row.Merchant), row.Amount, match.Status,
			matchedID, matchedDate, matchedMerchant, matchedAmount)
		if err != nil {
			log.Printf("insert statement transaction row: %v", err)
			writeError(w, http.StatusInternalServerError, "could not save statement transactions")
			return
		}
	}
	for _, candidate := range candidates {
		transactionDate, _ := time.Parse("2006-01-02", candidate.Date)
		if transactionDate.Before(periodStartDate) || transactionDate.After(periodEndDate) || matchedPlaidIDs[candidate.TransactionID] {
			continue
		}
		issueType := "plaid_only"
		if ambiguousPlaidIDs[candidate.TransactionID] {
			issueType = "ambiguous_plaid"
		}
		_, err := tx.Exec(r.Context(), `
			INSERT INTO statement_reconciliation_issues (
				statement_id, plaid_transaction_id, transaction_date, merchant, amount, issue_type
			) VALUES ($1,$2,$3,$4,$5,$6)`,
			statementID, candidate.TransactionID, candidate.Date, candidate.Merchant, candidate.Amount, issueType)
		if err != nil {
			log.Printf("insert statement reconciliation discrepancy: %v", err)
			writeError(w, http.StatusInternalServerError, "could not save statement reconciliation results")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("commit imported statement: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save statement import")
		return
	}
	summary, err := a.loadImportedStatementSummary(r.Context(), statementID)
	if err != nil {
		log.Printf("load imported statement summary: %v", err)
		writeError(w, http.StatusInternalServerError, "statement was imported but its summary could not be loaded")
		return
	}
	writeJSON(w, http.StatusCreated, summary)
}

func (a *app) reviewImportedStatement(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Decision string `json:"decision"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid statement review decision")
		return
	}
	decision, err := validateStatementReviewDecision(input.Decision)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tag, err := a.db.Exec(r.Context(), `
		UPDATE imported_statements
		SET review_status=$1, reviewed_at=NOW(), reviewed_by=$2
		WHERE statement_id=$3 AND review_status='pending_review'`,
		decision, currentPrincipal(r).ID, r.PathValue("statementID"))
	if err != nil {
		log.Printf("save statement review decision: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save statement review")
		return
	}
	if tag.RowsAffected() == 0 {
		var currentStatus string
		err := a.db.QueryRow(r.Context(),
			"SELECT review_status FROM imported_statements WHERE statement_id=$1",
			r.PathValue("statementID"),
		).Scan(&currentStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "statement import not found")
			return
		}
		if err != nil {
			log.Printf("check statement review status: %v", err)
			writeError(w, http.StatusInternalServerError, "could not save statement review")
			return
		}
		writeError(w, http.StatusConflict, "statement has already been reviewed")
		return
	}
	summary, err := a.loadImportedStatementSummary(r.Context(), r.PathValue("statementID"))
	if err != nil {
		log.Printf("load reviewed statement summary: %v", err)
		writeError(w, http.StatusInternalServerError, "statement review was saved but its summary could not be loaded")
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func validateStatementReviewDecision(decision string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "approved", "rejected":
		return strings.ToLower(strings.TrimSpace(decision)), nil
	default:
		return "", errors.New("decision must be approved or rejected")
	}
}

func (a *app) listImportedStatements(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), importedStatementSummaryQuery+` ORDER BY imported_at DESC`)
	if err != nil {
		log.Printf("query imported statements: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load statement history")
		return
	}
	defer rows.Close()
	statements := make([]importedStatementSummary, 0)
	for rows.Next() {
		var summary importedStatementSummary
		if err := scanImportedStatementSummary(rows, &summary); err != nil {
			log.Printf("scan imported statement summary: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load statement history")
			return
		}
		statements = append(statements, summary)
	}
	if err := rows.Err(); err != nil {
		log.Printf("iterate imported statement summaries: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load statement history")
		return
	}
	writeJSON(w, http.StatusOK, statements)
}

func (a *app) getImportedStatement(w http.ResponseWriter, r *http.Request) {
	summary, err := a.loadImportedStatementSummary(r.Context(), r.PathValue("statementID"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "statement import not found")
		return
	}
	if err != nil {
		log.Printf("load imported statement: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load statement import")
		return
	}
	rows, err := a.db.Query(r.Context(), `
		SELECT statement_transaction_id, transaction_date::text, merchant, amount,
		       match_status, matched_plaid_transaction_id, matched_plaid_date::text,
		       matched_plaid_merchant, matched_plaid_amount
		FROM statement_import_transactions
		WHERE statement_id=$1
		ORDER BY row_number`, summary.StatementID)
	if err != nil {
		log.Printf("query statement transaction rows: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load statement transactions")
		return
	}
	defer rows.Close()
	transactions := make([]statementImportTransaction, 0)
	for rows.Next() {
		var item statementImportTransaction
		if err := rows.Scan(&item.TransactionID, &item.Date, &item.Merchant, &item.Amount, &item.MatchStatus,
			&item.MatchedPlaidTransactionID, &item.MatchedPlaidDate, &item.MatchedPlaidMerchant, &item.MatchedPlaidAmount); err != nil {
			log.Printf("scan statement transaction row: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load statement transactions")
			return
		}
		transactions = append(transactions, item)
	}
	if err := rows.Err(); err != nil {
		log.Printf("iterate statement transaction rows: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load statement transactions")
		return
	}
	rows.Close()
	issueRows, err := a.db.Query(r.Context(), `
		SELECT discrepancy_id, plaid_transaction_id, transaction_date::text, merchant, amount, issue_type
		FROM statement_reconciliation_issues
		WHERE statement_id=$1
		ORDER BY transaction_date, discrepancy_id`, summary.StatementID)
	if err != nil {
		log.Printf("query statement discrepancies: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load statement discrepancies")
		return
	}
	defer issueRows.Close()
	discrepancies := make([]statementDiscrepancy, 0)
	for issueRows.Next() {
		var issue statementDiscrepancy
		if err := issueRows.Scan(&issue.DiscrepancyID, &issue.TransactionID, &issue.Date, &issue.Merchant, &issue.Amount, &issue.IssueType); err != nil {
			log.Printf("scan statement discrepancy: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load statement discrepancies")
			return
		}
		discrepancies = append(discrepancies, issue)
	}
	if err := issueRows.Err(); err != nil {
		log.Printf("iterate statement discrepancies: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load statement discrepancies")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"statement":     summary,
		"transactions":  transactions,
		"discrepancies": discrepancies,
	})
}

func (a *app) downloadImportedStatementSpreadsheet(w http.ResponseWriter, r *http.Request) {
	var spreadsheet []byte
	err := a.db.QueryRow(r.Context(), `
		SELECT spreadsheet FROM imported_statements WHERE statement_id=$1`,
		r.PathValue("statementID"),
	).Scan(&spreadsheet)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "statement import not found")
		return
	}
	if err != nil {
		log.Printf("load imported statement spreadsheet: %v", err)
		writeError(w, http.StatusInternalServerError, "could not download statement spreadsheet")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="statement-%s.xlsx"`, r.PathValue("statementID")))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(spreadsheet)
}

const importedStatementSummaryQuery = `
	SELECT statement_id, account_id, account_name, parser_id, review_status, period_start::text, period_end::text, imported_at,
	       (SELECT COUNT(*) FROM statement_import_transactions t WHERE t.statement_id=s.statement_id),
	       (SELECT COUNT(*) FROM statement_import_transactions t WHERE t.statement_id=s.statement_id AND t.match_status='matched'),
	       (SELECT COUNT(*) FROM statement_import_transactions t WHERE t.statement_id=s.statement_id AND t.match_status='statement_only'),
	       (SELECT COUNT(*) FROM statement_import_transactions t WHERE t.statement_id=s.statement_id AND t.match_status='ambiguous'),
	       (SELECT COUNT(*) FROM statement_reconciliation_issues i WHERE i.statement_id=s.statement_id AND i.issue_type='plaid_only'),
	       (SELECT COUNT(*) FROM statement_reconciliation_issues i WHERE i.statement_id=s.statement_id AND i.issue_type='ambiguous_plaid')
	FROM imported_statements s`

type statementSummaryRow interface {
	Scan(dest ...any) error
}

func scanImportedStatementSummary(row statementSummaryRow, summary *importedStatementSummary) error {
	return row.Scan(
		&summary.StatementID, &summary.AccountID, &summary.AccountName, &summary.ParserID, &summary.ReviewStatus,
		&summary.PeriodStart, &summary.PeriodEnd, &summary.ImportedAt, &summary.TransactionCount,
		&summary.MatchedCount, &summary.StatementOnlyCount, &summary.AmbiguousCount,
		&summary.PlaidOnlyCount, &summary.AmbiguousPlaidCount,
	)
}

func (a *app) loadImportedStatementSummary(ctx context.Context, statementID string) (importedStatementSummary, error) {
	var summary importedStatementSummary
	err := a.db.QueryRow(ctx, importedStatementSummaryQuery+` WHERE statement_id=$1`, statementID).
		Scan(
			&summary.StatementID, &summary.AccountID, &summary.AccountName, &summary.ParserID, &summary.ReviewStatus,
			&summary.PeriodStart, &summary.PeriodEnd, &summary.ImportedAt, &summary.TransactionCount,
			&summary.MatchedCount, &summary.StatementOnlyCount, &summary.AmbiguousCount,
			&summary.PlaidOnlyCount, &summary.AmbiguousPlaidCount,
		)
	return summary, err
}

func (a *app) loadPlaidStatementTransactions(ctx context.Context, accountID, periodStart, periodEnd string) ([]plaidStatementTransaction, error) {
	start, err := time.Parse("2006-01-02", periodStart)
	if err != nil {
		return nil, err
	}
	end, err := time.Parse("2006-01-02", periodEnd)
	if err != nil {
		return nil, err
	}
	rows, err := a.db.Query(ctx, `
		SELECT transaction_id, transaction_date::text, COALESCE(NULLIF(merchant_name, ''), name), amount
		FROM transactions
		WHERE account_id=$1 AND NOT pending
		  AND transaction_date BETWEEN $2::date AND $3::date
		ORDER BY transaction_date, transaction_id`,
		accountID, start.AddDate(0, 0, -2).Format("2006-01-02"), end.AddDate(0, 0, 2).Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	transactions := make([]plaidStatementTransaction, 0)
	for rows.Next() {
		var item plaidStatementTransaction
		if err := rows.Scan(&item.TransactionID, &item.Date, &item.Merchant, &item.Amount); err != nil {
			return nil, err
		}
		transactions = append(transactions, item)
	}
	return transactions, rows.Err()
}

func reconcileStatementRows(rows []parsedStatementTransaction, plaidRows []plaidStatementTransaction) ([]statementMatch, map[string]bool) {
	candidatesByStatement := make([][]int, len(rows))
	for statementIndex, statementRow := range rows {
		statementDate, _ := time.Parse("2006-01-02", statementRow.Date)
		for plaidIndex, plaidRow := range plaidRows {
			plaidDate, _ := time.Parse("2006-01-02", plaidRow.Date)
			if cents(statementRow.Amount) != cents(plaidRow.Amount) || math.Abs(statementDate.Sub(plaidDate).Hours()/24) > 2 {
				continue
			}
			candidatesByStatement[statementIndex] = append(candidatesByStatement[statementIndex], plaidIndex)
		}
	}

	matches := make([]statementMatch, len(rows))
	assignedPlaid := make(map[int]int)
	for {
		reverseCount := make(map[int]int)
		for statementIndex, candidateIndexes := range candidatesByStatement {
			if matches[statementIndex].Status != "" {
				continue
			}
			for _, plaidIndex := range candidateIndexes {
				if _, assigned := assignedPlaid[plaidIndex]; !assigned {
					reverseCount[plaidIndex]++
				}
			}
		}
		type pair struct{ statementIndex, plaidIndex int }
		uniquePairs := make([]pair, 0)
		for statementIndex, candidateIndexes := range candidatesByStatement {
			if matches[statementIndex].Status != "" {
				continue
			}
			available := -1
			availableCount := 0
			for _, plaidIndex := range candidateIndexes {
				if _, assigned := assignedPlaid[plaidIndex]; assigned {
					continue
				}
				available, availableCount = plaidIndex, availableCount+1
			}
			if availableCount == 1 && reverseCount[available] == 1 {
				uniquePairs = append(uniquePairs, pair{statementIndex, available})
			}
		}
		if len(uniquePairs) == 0 {
			break
		}
		for _, match := range uniquePairs {
			copy := plaidRows[match.plaidIndex]
			matches[match.statementIndex] = statementMatch{Status: "matched", Plaid: &copy}
			assignedPlaid[match.plaidIndex] = match.statementIndex
		}
	}

	ambiguousPlaidIDs := make(map[string]bool)
	for statementIndex, candidateIndexes := range candidatesByStatement {
		if matches[statementIndex].Status != "" {
			continue
		}
		if len(candidateIndexes) > 0 {
			matches[statementIndex].Status = "ambiguous"
			for _, plaidIndex := range candidateIndexes {
				if _, assigned := assignedPlaid[plaidIndex]; !assigned {
					ambiguousPlaidIDs[plaidRows[plaidIndex].TransactionID] = true
				}
			}
		} else {
			matches[statementIndex].Status = "statement_only"
		}
	}
	return matches, ambiguousPlaidIDs
}

func cents(amount float64) int64 {
	return int64(math.Round(amount * 100))
}

func validateStatementPeriod(start, end string) (string, string, error) {
	if start == "" || end == "" {
		return "", "", errors.New("both statement period dates are required")
	}
	startDate, err := time.Parse("2006-01-02", start)
	if err != nil {
		return "", "", errors.New("statement start date must be YYYY-MM-DD")
	}
	endDate, err := time.Parse("2006-01-02", end)
	if err != nil {
		return "", "", errors.New("statement end date must be YYYY-MM-DD")
	}
	if startDate.After(endDate) {
		return "", "", errors.New("statement start date must be on or before its end date")
	}
	return start, end, nil
}

func resolveStatementPeriod(parsed parsedStatement, overrideStart, overrideEnd string) (string, string, error) {
	if overrideStart != "" || overrideEnd != "" {
		return validateStatementPeriod(overrideStart, overrideEnd)
	}
	if parsed.PeriodStart == nil || parsed.PeriodEnd == nil {
		return "", "", errors.New("the statement period could not be read; upload again with both period dates")
	}
	return validateStatementPeriod(*parsed.PeriodStart, *parsed.PeriodEnd)
}

func newStatementID() (string, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	return hex.EncodeToString(id), nil
}

func isBundledStatementParser(parserID string) bool {
	for _, parser := range bundledStatementParsers {
		if parser.ParserID == parserID {
			return true
		}
	}
	return false
}
