package main

import (
	"testing"
	"time"
)

func TestSpendCycleStart(t *testing.T) {
	tests := []struct {
		date string
		want string
	}{
		{date: "2026-10-21", want: "2026-10-04"},
		{date: "2026-10-03", want: "2026-09-04"},
		{date: "2027-01-02", want: "2026-12-04"},
	}
	for _, test := range tests {
		date, err := time.Parse("2006-01-02", test.date)
		if err != nil {
			t.Fatal(err)
		}
		if got := spendCycleStart(date).Format("2006-01-02"); got != test.want {
			t.Errorf("spendCycleStart(%s) = %s, want %s", test.date, got, test.want)
		}
	}
}

func TestCalculateIncomeProjectionsIncludesCurrentAndNextTwelveCycles(t *testing.T) {
	today := time.Date(2026, time.October, 21, 0, 0, 0, 0, time.UTC)
	periods := calculateIncomeProjections(projectionSettings{}, today)
	if len(periods) != 13 {
		t.Fatalf("projection periods = %d, want current plus 12 future cycles", len(periods))
	}
	if periods[0].SpendMonthStart != "2026-10-04" || periods[12].SpendMonthStart != "2027-10-04" {
		t.Fatalf("projection window = %s through %s, want 2026-10-04 through 2027-10-04", periods[0].SpendMonthStart, periods[12].SpendMonthStart)
	}
}

func TestCalculateIncomeProjectionPayTimingAndHoliday(t *testing.T) {
	amount := 2000.0
	anchor := "2026-10-08"
	settings := projectionSettings{
		BiweeklyPayAmount:    &amount,
		BiweeklyPaydayAnchor: &anchor,
		WifePayRates:         map[int]float64{10: 900, 11: 1000},
		FederalHolidays:      []string{"labor_day"},
	}
	start := time.Date(2026, time.October, 4, 0, 0, 0, 0, time.UTC)
	projection := calculateIncomeProjection(settings, start)

	if projection.YourPayTotal == nil || *projection.YourPayTotal != 4000 {
		t.Fatalf("your pay total = %v, want 4000", projection.YourPayTotal)
	}
	if projection.WifePayTotal == nil || *projection.WifePayTotal != 1900 {
		t.Fatalf("wife pay total = %v, want 1900", projection.WifePayTotal)
	}
	if projection.ProjectedExpendableIncome == nil || *projection.ProjectedExpendableIncome != 5900 {
		t.Fatalf("total projection = %v, want 5900", projection.ProjectedExpendableIncome)
	}
	if len(projection.WifePayPeriods) != 2 {
		t.Fatalf("wife pay periods = %d, want 2", len(projection.WifePayPeriods))
	}
	first := projection.WifePayPeriods[0]
	if first.PayDate != "2026-10-15" || first.WorkPeriodStart != "2026-09-01" || first.WorkPeriodEnd != "2026-09-15" || first.Workdays != 10 {
		t.Fatalf("first wife pay period = %+v, want delayed Sep 1-15 period with 10 workdays paid Oct 15", first)
	}
	second := projection.WifePayPeriods[1]
	if second.PayDate != "2026-10-31" || second.WorkPeriodStart != "2026-09-16" || second.WorkPeriodEnd != "2026-09-30" || second.Workdays != 11 {
		t.Fatalf("second wife pay period = %+v, want delayed Sep 16-30 period with 11 workdays paid Oct 31", second)
	}
}

func TestObservedHolidayAndPlannedDayOffReduceWorkdays(t *testing.T) {
	start := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.July, 5, 0, 0, 0, 0, time.UTC)
	selected := selectedFederalHolidayDates(start, end, map[string]struct{}{"independence_day": {}})
	planned := map[string]struct{}{"2026-07-02": {}}
	if got := countWorkdays(start, end, selected, planned); got != 1 {
		t.Fatalf("workday count = %d, want 1 after observed July 3 holiday and July 2 planned day off", got)
	}
}

func TestPreviousMonthFederalHolidayIsAppliedToWifePayPeriod(t *testing.T) {
	amount := 1000.0
	anchor := "2026-02-05"
	settings := projectionSettings{
		BiweeklyPayAmount:    &amount,
		BiweeklyPaydayAnchor: &anchor,
		WifePayRates:         map[int]float64{10: 100, 11: 110},
		FederalHolidays:      []string{"new_years_day"},
	}
	projection := calculateIncomeProjection(settings, time.Date(2026, time.February, 4, 0, 0, 0, 0, time.UTC))
	if len(projection.WifePayPeriods) == 0 || projection.WifePayPeriods[0].Workdays != 10 {
		t.Fatalf("first wife's pay period = %+v, want Jan 1-15 period with New Year's Day excluded", projection.WifePayPeriods)
	}
	if projection.WifePayTotal == nil || *projection.WifePayTotal != 210 {
		t.Fatalf("wife pay total = %v, want 210", projection.WifePayTotal)
	}
}

func TestBiweeklyPayIsNotShiftedForFederalHoliday(t *testing.T) {
	amount := 1500.0
	anchor := "2026-11-26"
	settings := projectionSettings{
		BiweeklyPayAmount:    &amount,
		BiweeklyPaydayAnchor: &anchor,
		FederalHolidays:      []string{"thanksgiving_day"},
	}
	projection := calculateIncomeProjection(settings, time.Date(2026, time.November, 4, 0, 0, 0, 0, time.UTC))
	if projection.YourPayTotal == nil || *projection.YourPayTotal != 3000 {
		t.Fatalf("your pay total = %v, want two fixed-date deposits including Thanksgiving", projection.YourPayTotal)
	}
}

func TestMissingWifeRateLeavesProjectionIncomplete(t *testing.T) {
	amount := 1000.0
	anchor := "2026-10-08"
	daysOff := []string{
		"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04",
		"2026-09-07", "2026-09-08", "2026-09-09", "2026-09-10",
		"2026-09-11", "2026-09-14", "2026-09-15",
	}
	settings := projectionSettings{
		BiweeklyPayAmount:    &amount,
		BiweeklyPaydayAnchor: &anchor,
		WifePayRates:         map[int]float64{11: 500},
		DaysOff:              daysOff,
	}
	projection := calculateIncomeProjection(settings, time.Date(2026, time.October, 4, 0, 0, 0, 0, time.UTC))
	if projection.ProjectedExpendableIncome != nil {
		t.Fatalf("projection = %v, want incomplete projection", projection.ProjectedExpendableIncome)
	}
	if len(projection.MissingWorkdayCounts) != 1 || projection.MissingWorkdayCounts[0] != 0 {
		t.Fatalf("missing workday counts = %v, want [0]", projection.MissingWorkdayCounts)
	}
}
