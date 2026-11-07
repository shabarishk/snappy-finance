package main

import (
	"strings"
	"testing"
)

func TestReconcileStatementRowsMatchesUniqueAmountWithinTwoDays(t *testing.T) {
	statementRows := []parsedStatementTransaction{
		{Date: "2026-01-03", Merchant: "Coffee shop", Amount: -12.50},
		{Date: "2026-01-12", Merchant: "Utility company", Amount: -80.00},
	}
	plaidRows := []plaidStatementTransaction{
		{TransactionID: "match", Date: "2026-01-01", Merchant: "COFFEE", Amount: 12.50},
		{TransactionID: "outside-window", Date: "2026-01-15", Merchant: "Utility", Amount: 80.00},
	}

	matches, ambiguousPlaid := reconcileStatementRows(statementRows, plaidRows)
	if matches[0].Status != "matched" || matches[0].Plaid == nil || matches[0].Plaid.TransactionID != "match" {
		t.Fatalf("first match = %+v, want the unique transaction within two days", matches[0])
	}
	if matches[1].Status != "statement_only" {
		t.Fatalf("second match status = %q, want statement_only", matches[1].Status)
	}
	if ambiguousPlaid["match"] || ambiguousPlaid["outside-window"] {
		t.Fatalf("unexpected ambiguous Plaid transactions: %v", ambiguousPlaid)
	}
}

func TestReconcileStatementRowsMarksDuplicateCandidatesAmbiguous(t *testing.T) {
	statementRows := []parsedStatementTransaction{
		{Date: "2026-02-01", Merchant: "Store one", Amount: -25.00},
		{Date: "2026-02-02", Merchant: "Store two", Amount: -25.00},
	}
	plaidRows := []plaidStatementTransaction{
		{TransactionID: "plaid-one", Date: "2026-02-02", Merchant: "Store", Amount: 25.00},
	}

	matches, ambiguousPlaid := reconcileStatementRows(statementRows, plaidRows)
	for index, match := range matches {
		if match.Status != "ambiguous" || match.Plaid != nil {
			t.Errorf("match %d = %+v, want ambiguous with no automatic Plaid link", index, match)
		}
	}
	if !ambiguousPlaid["plaid-one"] {
		t.Fatal("the Plaid transaction should be flagged as an ambiguous candidate")
	}
}

func TestReconcileStatementRowsConvertsStatementSignToPlaidSign(t *testing.T) {
	statementRows := []parsedStatementTransaction{
		{Date: "2026-03-01", Merchant: "Payment", Amount: -50.00},
	}
	plaidRows := []plaidStatementTransaction{
		{TransactionID: "plaid-sign", Date: "2026-03-01", Merchant: "Payment", Amount: 50.00},
	}

	matches, _ := reconcileStatementRows(statementRows, plaidRows)
	if matches[0].Status != "matched" {
		t.Fatalf("match status = %q, want matched after sign conversion", matches[0].Status)
	}
}

func TestReconcileStatementRowsPreservingMatchesKeepsPriorMatchesAndReservesPlaidRows(t *testing.T) {
	statementRows := []parsedStatementTransaction{
		{Date: "2026-03-01", Merchant: "Coffee shop", Amount: -12.50},
		{Date: "2026-03-02", Merchant: "Cafe", Amount: -12.50},
	}
	plaidRows := []plaidStatementTransaction{
		{TransactionID: "previously-selected", Date: "2026-03-01", Merchant: "COFFEE", Amount: 12.50},
		{TransactionID: "new-match", Date: "2026-03-02", Merchant: "CAFE", Amount: 12.50},
	}
	existingMatches := []existingStatementMatch{
		{
			RowNumber:    1,
			StatementRow: parsedStatementTransaction{Date: "2026-03-01", Merchant: "Coffee shop", Amount: -12.50},
			Plaid:        plaidRows[0],
		},
	}

	matches, ambiguousPlaid := reconcileStatementRowsPreservingMatches(
		statementRows,
		plaidRows,
		existingMatches,
	)
	if matches[0].Status != "matched" || matches[0].Plaid == nil ||
		matches[0].Plaid.TransactionID != "previously-selected" {
		t.Fatalf("first match = %+v, want the previous match preserved", matches[0])
	}
	if matches[1].Status != "matched" || matches[1].Plaid == nil ||
		matches[1].Plaid.TransactionID != "new-match" {
		t.Fatalf("second match = %+v, want the remaining Plaid transaction matched", matches[1])
	}
	if len(ambiguousPlaid) != 0 {
		t.Fatalf("unexpected ambiguous Plaid transactions: %v", ambiguousPlaid)
	}
}

func TestReconcileStatementRowsPreservingMatchesRechecksChangedRows(t *testing.T) {
	statementRows := []parsedStatementTransaction{
		{Date: "2026-03-01", Merchant: "Coffee shop", Amount: -13.00},
	}
	plaidRows := []plaidStatementTransaction{
		{TransactionID: "new-match", Date: "2026-03-01", Merchant: "COFFEE", Amount: 13.00},
	}
	existingMatches := []existingStatementMatch{
		{
			RowNumber:    1,
			StatementRow: parsedStatementTransaction{Date: "2026-03-01", Merchant: "Coffee shop", Amount: -12.50},
			Plaid:        plaidStatementTransaction{TransactionID: "previous-match"},
		},
	}

	matches, _ := reconcileStatementRowsPreservingMatches(statementRows, plaidRows, existingMatches)
	if matches[0].Status != "matched" || matches[0].Plaid == nil ||
		matches[0].Plaid.TransactionID != "new-match" {
		t.Fatalf("match = %+v, want changed row reconciled against current Plaid transactions", matches[0])
	}
}

func TestValidateStatementPeriod(t *testing.T) {
	if start, end, err := validateStatementPeriod("2026-04-01", "2026-04-30"); err != nil ||
		start != "2026-04-01" || end != "2026-04-30" {
		t.Fatalf("validateStatementPeriod = (%q, %q, %v), want valid April range", start, end, err)
	}
	for _, invalid := range [][2]string{
		{"2026-02-30", "2026-03-01"},
		{"2026-05-01", "2026-04-30"},
		{"", "2026-04-30"},
	} {
		if _, _, err := validateStatementPeriod(invalid[0], invalid[1]); err == nil {
			t.Errorf("validateStatementPeriod(%q, %q) succeeded, want error", invalid[0], invalid[1])
		}
	}
}

func TestValidateStatementReviewDecision(t *testing.T) {
	for _, decision := range []string{"approved", "rejected", " APPROVED "} {
		if got, err := validateStatementReviewDecision(decision); err != nil || got != strings.ToLower(strings.TrimSpace(decision)) {
			t.Errorf("validateStatementReviewDecision(%q) = (%q, %v)", decision, got, err)
		}
	}
	for _, decision := range []string{"", "pending_review", "approve", "ignored"} {
		if _, err := validateStatementReviewDecision(decision); err == nil {
			t.Errorf("validateStatementReviewDecision(%q) succeeded, want error", decision)
		}
	}
}

func TestStatementParserScriptForBundledParsers(t *testing.T) {
	cfg := config{
		statementParserScript: "scripts/parse_bilt_app.py",
		chaseParserScript:     "scripts/parse_chase.py",
		amexGoldParserScript:  "scripts/parse_amex_gold.py",
	}
	for parserID, want := range map[string]string{
		"bilt":      cfg.statementParserScript,
		"chase":     cfg.chaseParserScript,
		"amex_gold": cfg.amexGoldParserScript,
	} {
		got, ok := statementParserScriptFor(parserID, cfg)
		if !ok || got != want {
			t.Errorf("statementParserScriptFor(%q) = (%q, %t), want (%q, true)", parserID, got, ok, want)
		}
	}
	if _, ok := statementParserScriptFor("unknown", cfg); ok {
		t.Fatal("unknown statement parser should not have a script")
	}
}
