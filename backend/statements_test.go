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
