package main

import (
	"testing"
	"time"
)

func TestTransactionSpendMonthForCashUsesTransactionDate(t *testing.T) {
	for _, test := range []struct {
		date string
		want string
	}{
		{date: "2026-10-03", want: "2026-09"},
		{date: "2026-10-04", want: "2026-10"},
	} {
		date, err := time.Parse("2006-01-02", test.date)
		if err != nil {
			t.Fatal(err)
		}
		got, included := transactionSpendMonth(date, "depository", nil, nil)
		if !included || got != test.want {
			t.Errorf("transactionSpendMonth(%s, depository) = %q, %t; want %q, true", test.date, got, included, test.want)
		}
	}
}

func TestTransactionSpendMonthForCreditUsesStatementDueDate(t *testing.T) {
	closeDay, dueDay := int16(3), int16(1)
	for _, test := range []struct {
		date string
		want string
	}{
		{date: "2026-09-03", want: "2026-09"},
		{date: "2026-09-04", want: "2026-10"},
		{date: "2026-10-03", want: "2026-10"},
		{date: "2026-10-04", want: "2026-11"},
	} {
		date, err := time.Parse("2006-01-02", test.date)
		if err != nil {
			t.Fatal(err)
		}
		got, included := transactionSpendMonth(date, "credit", &closeDay, &dueDay)
		if !included || got != test.want {
			t.Errorf("transactionSpendMonth(%s, credit) = %q, %t; want %q, true", test.date, got, included, test.want)
		}
	}
}

func TestTransactionSpendMonthSkipsUnconfiguredCreditAccount(t *testing.T) {
	date := time.Date(2026, time.September, 4, 0, 0, 0, 0, time.UTC)
	if month, included := transactionSpendMonth(date, "credit", nil, nil); included {
		t.Fatalf("unconfigured card transaction assigned to %s", month)
	}
}

func TestDayInMonthClampsLongMonths(t *testing.T) {
	got := dayInMonth(2026, time.February, 31)
	if got.Format("2006-01-02") != "2026-02-28" {
		t.Fatalf("dayInMonth = %s, want 2026-02-28", got.Format("2006-01-02"))
	}
}
