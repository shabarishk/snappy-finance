package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

type projectionSettings struct {
	BiweeklyPayAmount    *float64        `json:"biweekly_pay_amount"`
	BiweeklyPaydayAnchor *string         `json:"biweekly_payday_anchor"`
	WifePayRates         map[int]float64 `json:"wife_pay_rates"`
	FederalHolidays      []string        `json:"federal_holidays"`
	DaysOff              []string        `json:"days_off"`
}

type federalHolidayOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type projectionPayPeriod struct {
	PayDate         string   `json:"pay_date"`
	WorkPeriodStart string   `json:"work_period_start"`
	WorkPeriodEnd   string   `json:"work_period_end"`
	Workdays        int      `json:"workdays"`
	Amount          *float64 `json:"amount"`
}

type incomeProjectionPeriod struct {
	SpendMonthStart           string                `json:"spend_month_start"`
	SpendMonthEnd             string                `json:"spend_month_end"`
	YourPayTotal              *float64              `json:"your_pay_total"`
	WifePayTotal              *float64              `json:"wife_pay_total"`
	ProjectedExpendableIncome *float64              `json:"projected_expendable_income"`
	WifePayPeriods            []projectionPayPeriod `json:"wife_pay_periods"`
	MissingWorkdayCounts      []int                 `json:"missing_workday_counts"`
}

var federalHolidayOptions = []federalHolidayOption{
	{ID: "new_years_day", Label: "New Year's Day"},
	{ID: "martin_luther_king_day", Label: "Martin Luther King Jr. Day"},
	{ID: "washingtons_birthday", Label: "Washington's Birthday"},
	{ID: "memorial_day", Label: "Memorial Day"},
	{ID: "juneteenth", Label: "Juneteenth National Independence Day"},
	{ID: "independence_day", Label: "Independence Day"},
	{ID: "labor_day", Label: "Labor Day"},
	{ID: "columbus_day", Label: "Columbus Day"},
	{ID: "veterans_day", Label: "Veterans Day"},
	{ID: "thanksgiving_day", Label: "Thanksgiving Day"},
	{ID: "christmas_day", Label: "Christmas Day"},
}

func (a *app) getProjections(w http.ResponseWriter, r *http.Request) {
	settings, periods, err := a.refreshOpenIncomeProjections(r.Context())
	if err != nil {
		log.Printf("calculate income projections: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load income projections")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":                settings,
		"federal_holiday_options": federalHolidayOptions,
		"periods":                 periods,
	})
}

func (a *app) refreshOpenIncomeProjections(ctx context.Context) (projectionSettings, []incomeProjectionPeriod, error) {
	if err := a.freezeExpiredProjectedIncome(ctx); err != nil {
		return projectionSettings{}, nil, err
	}
	settings, err := a.loadProjectionSettings(ctx)
	if err != nil {
		return settings, nil, err
	}
	var today time.Time
	if err := a.db.QueryRow(ctx, "SELECT CURRENT_DATE").Scan(&today); err != nil {
		return settings, nil, err
	}
	today = dateOnly(today)
	periods := calculateIncomeProjections(settings, today)
	if err := a.persistOpenProjections(ctx, today, periods); err != nil {
		return settings, nil, err
	}
	return settings, periods, nil
}

func calculateIncomeProjections(settings projectionSettings, today time.Time) []incomeProjectionPeriod {
	firstPeriod := spendCycleStart(today)
	periods := make([]incomeProjectionPeriod, 0, 13)
	for index := 0; index <= 12; index++ {
		periodStart := firstPeriod.AddDate(0, index, 0)
		periods = append(periods, calculateIncomeProjection(settings, periodStart))
	}
	return periods
}

func (a *app) saveProjectionSettings(w http.ResponseWriter, r *http.Request) {
	var input projectionSettings
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if input.BiweeklyPayAmount == nil || *input.BiweeklyPayAmount < 0 {
		writeError(w, http.StatusBadRequest, "your net biweekly pay amount is required and must be zero or greater")
		return
	}
	if input.BiweeklyPaydayAnchor == nil {
		writeError(w, http.StatusBadRequest, "a biweekly payday anchor date is required")
		return
	}
	anchor, err := time.Parse("2006-01-02", *input.BiweeklyPaydayAnchor)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid biweekly payday anchor date")
		return
	}
	if anchor.Weekday() != time.Thursday {
		writeError(w, http.StatusBadRequest, "the biweekly payday anchor must be a Thursday")
		return
	}
	for workdays := 8; workdays <= 12; workdays++ {
		if _, exists := input.WifePayRates[workdays]; !exists {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("a net pay amount for %d workdays is required", workdays))
			return
		}
	}
	for workdays, amount := range input.WifePayRates {
		if workdays < 0 || workdays > 12 || amount < 0 {
			writeError(w, http.StatusBadRequest, "wife's pay rates must cover 0 to 12 workdays with non-negative amounts")
			return
		}
	}
	knownHolidays := make(map[string]struct{}, len(federalHolidayOptions))
	for _, option := range federalHolidayOptions {
		knownHolidays[option.ID] = struct{}{}
	}
	holidays := make([]string, 0, len(input.FederalHolidays))
	seenHolidays := make(map[string]struct{}, len(input.FederalHolidays))
	for _, holiday := range input.FederalHolidays {
		if _, exists := knownHolidays[holiday]; !exists {
			writeError(w, http.StatusBadRequest, "unknown federal holiday")
			return
		}
		if _, exists := seenHolidays[holiday]; !exists {
			seenHolidays[holiday] = struct{}{}
			holidays = append(holidays, holiday)
		}
	}
	daysOff := make([]time.Time, 0, len(input.DaysOff))
	seenDaysOff := make(map[string]struct{}, len(input.DaysOff))
	for _, value := range input.DaysOff {
		dayOff, err := time.Parse("2006-01-02", value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid planned day off")
			return
		}
		key := dayOff.Format("2006-01-02")
		if _, exists := seenDaysOff[key]; exists {
			continue
		}
		seenDaysOff[key] = struct{}{}
		daysOff = append(daysOff, dayOff)
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		log.Printf("begin projection settings update: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save projection settings")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		UPDATE income_projection_settings
		SET biweekly_pay_amount = $1, biweekly_payday_anchor = $2, updated_at = NOW()
		WHERE setting_id = 1`, *input.BiweeklyPayAmount, anchor); err != nil {
		log.Printf("save biweekly pay settings: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save projection settings")
		return
	}
	if _, err := tx.Exec(r.Context(), "DELETE FROM projection_wife_pay_rates"); err != nil {
		log.Printf("replace wife's pay rates: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save projection settings")
		return
	}
	for workdays, amount := range input.WifePayRates {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO projection_wife_pay_rates (workday_count, net_pay) VALUES ($1, $2)`,
			workdays, amount); err != nil {
			log.Printf("save wife's pay rate: %v", err)
			writeError(w, http.StatusInternalServerError, "could not save projection settings")
			return
		}
	}
	if _, err := tx.Exec(r.Context(), "DELETE FROM projection_federal_holidays"); err != nil {
		log.Printf("replace projection federal holidays: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save projection settings")
		return
	}
	for _, holiday := range holidays {
		if _, err := tx.Exec(r.Context(),
			"INSERT INTO projection_federal_holidays (holiday_id) VALUES ($1)", holiday); err != nil {
			log.Printf("save projection federal holiday: %v", err)
			writeError(w, http.StatusInternalServerError, "could not save projection settings")
			return
		}
	}
	if _, err := tx.Exec(r.Context(), "DELETE FROM projection_days_off"); err != nil {
		log.Printf("replace planned projection days off: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save projection settings")
		return
	}
	for _, dayOff := range daysOff {
		if _, err := tx.Exec(r.Context(),
			"INSERT INTO projection_days_off (day_off) VALUES ($1)", dayOff); err != nil {
			log.Printf("save planned projection day off: %v", err)
			writeError(w, http.StatusInternalServerError, "could not save projection settings")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("commit projection settings: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save projection settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}

func (a *app) loadProjectionSettings(ctx context.Context) (projectionSettings, error) {
	settings := projectionSettings{
		WifePayRates:    make(map[int]float64),
		FederalHolidays: make([]string, 0),
		DaysOff:         make([]string, 0),
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return settings, err
	}
	defer tx.Rollback(ctx)
	var amount *float64
	var anchor *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT biweekly_pay_amount, biweekly_payday_anchor
		FROM income_projection_settings WHERE setting_id = 1`,
	).Scan(&amount, &anchor); err != nil {
		return settings, err
	}
	settings.BiweeklyPayAmount = amount
	if anchor != nil {
		value := anchor.Format("2006-01-02")
		settings.BiweeklyPaydayAnchor = &value
	}
	rows, err := tx.Query(ctx, `
		SELECT workday_count, net_pay FROM projection_wife_pay_rates ORDER BY workday_count`)
	if err != nil {
		return settings, err
	}
	for rows.Next() {
		var workdays int
		var pay float64
		if err := rows.Scan(&workdays, &pay); err != nil {
			rows.Close()
			return settings, err
		}
		settings.WifePayRates[workdays] = pay
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return settings, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `
		SELECT holiday_id FROM projection_federal_holidays ORDER BY holiday_id`)
	if err != nil {
		return settings, err
	}
	for rows.Next() {
		var holiday string
		if err := rows.Scan(&holiday); err != nil {
			rows.Close()
			return settings, err
		}
		settings.FederalHolidays = append(settings.FederalHolidays, holiday)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return settings, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, "SELECT day_off FROM projection_days_off ORDER BY day_off")
	if err != nil {
		return settings, err
	}
	for rows.Next() {
		var dayOff time.Time
		if err := rows.Scan(&dayOff); err != nil {
			rows.Close()
			return settings, err
		}
		settings.DaysOff = append(settings.DaysOff, dayOff.Format("2006-01-02"))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return settings, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return settings, err
	}
	return settings, nil
}

func (a *app) persistOpenProjections(ctx context.Context, today time.Time, periods []incomeProjectionPeriod) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, period := range periods {
		start, err := time.Parse("2006-01-02", period.SpendMonthStart)
		if err != nil {
			return err
		}
		end, err := time.Parse("2006-01-02", period.SpendMonthEnd)
		if err != nil {
			return err
		}
		if end.Before(today) {
			continue
		}
		if period.ProjectedExpendableIncome == nil {
			if _, err := tx.Exec(ctx, `
				DELETE FROM projected_expendable_income
				WHERE period_start = $1 AND frozen_at IS NULL`, start); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO projected_expendable_income (period_start, amount)
			VALUES ($1, $2)
			ON CONFLICT (period_start) DO UPDATE SET
				amount = EXCLUDED.amount, updated_at = NOW()
			WHERE projected_expendable_income.frozen_at IS NULL`,
			start, *period.ProjectedExpendableIncome); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (a *app) projectedIncomeForPeriod(ctx context.Context, periodStart, today time.Time) (*float64, error) {
	settings, err := a.loadProjectionSettings(ctx)
	if err != nil {
		return nil, err
	}
	period := calculateIncomeProjection(settings, periodStart)
	periodEnd := spendCycleEnd(periodStart)
	if periodEnd.Before(today) {
		var frozenAmount *float64
		err := a.db.QueryRow(ctx, `
			SELECT amount FROM projected_expendable_income
			WHERE period_start = $1 AND frozen_at IS NOT NULL`, periodStart).Scan(&frozenAmount)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		return frozenAmount, nil
	}
	if period.ProjectedExpendableIncome == nil {
		if _, err := a.db.Exec(ctx, `
			DELETE FROM projected_expendable_income
			WHERE period_start = $1 AND frozen_at IS NULL`, periodStart); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if _, err := a.db.Exec(ctx, `
		INSERT INTO projected_expendable_income (period_start, amount)
		VALUES ($1, $2)
		ON CONFLICT (period_start) DO UPDATE SET
			amount = EXCLUDED.amount, updated_at = NOW()
		WHERE projected_expendable_income.frozen_at IS NULL`,
		periodStart, *period.ProjectedExpendableIncome); err != nil {
		return nil, err
	}
	return period.ProjectedExpendableIncome, nil
}

func calculateIncomeProjection(settings projectionSettings, periodStart time.Time) incomeProjectionPeriod {
	periodStart = dateOnly(periodStart)
	periodEnd := spendCycleEnd(periodStart)
	projection := incomeProjectionPeriod{
		SpendMonthStart:      periodStart.Format("2006-01-02"),
		SpendMonthEnd:        periodEnd.Format("2006-01-02"),
		WifePayPeriods:       make([]projectionPayPeriod, 0, 2),
		MissingWorkdayCounts: make([]int, 0),
	}
	if settings.BiweeklyPayAmount != nil && settings.BiweeklyPaydayAnchor != nil {
		anchor, err := time.Parse("2006-01-02", *settings.BiweeklyPaydayAnchor)
		if err == nil {
			payday := firstPaydayOnOrAfter(anchor, periodStart)
			total := 0.0
			for !payday.After(periodEnd) {
				total += *settings.BiweeklyPayAmount
				payday = payday.AddDate(0, 0, 14)
			}
			projection.YourPayTotal = &total
		}
	}

	selectedHolidays := make(map[string]struct{}, len(settings.FederalHolidays))
	for _, holiday := range settings.FederalHolidays {
		selectedHolidays[holiday] = struct{}{}
	}
	holidayStart := time.Date(periodStart.Year(), periodStart.Month()-1, 1, 0, 0, 0, 0, time.UTC)
	holidayDates := selectedFederalHolidayDates(holidayStart, periodEnd, selectedHolidays)
	daysOff := make(map[string]struct{}, len(settings.DaysOff))
	for _, dayOff := range settings.DaysOff {
		daysOff[dayOff] = struct{}{}
	}
	missingCounts := make(map[int]struct{})
	wifeTotal := 0.0
	wifeComplete := true
	dueMonth := time.Date(periodStart.Year(), periodStart.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastDueMonth := time.Date(periodEnd.Year(), periodEnd.Month(), 1, 0, 0, 0, 0, time.UTC)
	for !dueMonth.After(lastDueMonth) {
		payDates := []time.Time{
			time.Date(dueMonth.Year(), dueMonth.Month(), 15, 0, 0, 0, 0, time.UTC),
			time.Date(dueMonth.Year(), dueMonth.Month()+1, 0, 0, 0, 0, 0, time.UTC),
		}
		workMonth := dueMonth.AddDate(0, -1, 0)
		for index, payDate := range payDates {
			if payDate.Before(periodStart) || payDate.After(periodEnd) {
				continue
			}
			workStart := time.Date(workMonth.Year(), workMonth.Month(), 1, 0, 0, 0, 0, time.UTC)
			workEnd := time.Date(workMonth.Year(), workMonth.Month(), 15, 0, 0, 0, 0, time.UTC)
			if index == 1 {
				workStart = time.Date(workMonth.Year(), workMonth.Month(), 16, 0, 0, 0, 0, time.UTC)
				workEnd = time.Date(workMonth.Year(), workMonth.Month()+1, 0, 0, 0, 0, 0, time.UTC)
			}
			workdays := countWorkdays(workStart, workEnd, holidayDates, daysOff)
			payPeriod := projectionPayPeriod{
				PayDate:         payDate.Format("2006-01-02"),
				WorkPeriodStart: workStart.Format("2006-01-02"),
				WorkPeriodEnd:   workEnd.Format("2006-01-02"),
				Workdays:        workdays,
			}
			if amount, exists := settings.WifePayRates[workdays]; exists {
				payPeriod.Amount = &amount
				wifeTotal += amount
			} else {
				wifeComplete = false
				missingCounts[workdays] = struct{}{}
			}
			projection.WifePayPeriods = append(projection.WifePayPeriods, payPeriod)
		}
		dueMonth = dueMonth.AddDate(0, 1, 0)
	}
	if wifeComplete {
		projection.WifePayTotal = &wifeTotal
	} else {
		for count := range missingCounts {
			projection.MissingWorkdayCounts = append(projection.MissingWorkdayCounts, count)
		}
		sort.Ints(projection.MissingWorkdayCounts)
	}
	if projection.YourPayTotal != nil && projection.WifePayTotal != nil {
		total := *projection.YourPayTotal + *projection.WifePayTotal
		projection.ProjectedExpendableIncome = &total
	}
	return projection
}

func spendCycleStart(date time.Time) time.Time {
	date = dateOnly(date)
	start := time.Date(date.Year(), date.Month(), 4, 0, 0, 0, 0, time.UTC)
	if date.Day() < 4 {
		start = start.AddDate(0, -1, 0)
	}
	return start
}

func spendCycleEnd(start time.Time) time.Time {
	return dateOnly(start).AddDate(0, 1, 0).AddDate(0, 0, -1)
}

func firstPaydayOnOrAfter(anchor, start time.Time) time.Time {
	anchor = dateOnly(anchor)
	start = dateOnly(start)
	if anchor.After(start) {
		days := int(anchor.Sub(start).Hours() / 24)
		periods := days / 14
		return anchor.AddDate(0, 0, -periods*14)
	}
	days := int(start.Sub(anchor).Hours() / 24)
	periods := (days + 13) / 14
	return anchor.AddDate(0, 0, periods*14)
}

func countWorkdays(start, end time.Time, holidayDates, daysOff map[string]struct{}) int {
	workdays := 0
	for day := dateOnly(start); !day.After(end); day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		key := day.Format("2006-01-02")
		if _, exists := holidayDates[key]; exists {
			continue
		}
		if _, exists := daysOff[key]; exists {
			continue
		}
		workdays++
	}
	return workdays
}

func selectedFederalHolidayDates(start, end time.Time, selected map[string]struct{}) map[string]struct{} {
	dates := make(map[string]struct{})
	for year := start.Year() - 1; year <= end.Year()+1; year++ {
		for holidayID, actualDate := range federalHolidaysForYear(year) {
			if _, exists := selected[holidayID]; !exists {
				continue
			}
			observed := observedFederalHoliday(actualDate)
			dates[observed.Format("2006-01-02")] = struct{}{}
		}
	}
	return dates
}

func federalHolidaysForYear(year int) map[string]time.Time {
	return map[string]time.Time{
		"new_years_day":          time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC),
		"martin_luther_king_day": nthWeekday(year, time.January, time.Monday, 3),
		"washingtons_birthday":   nthWeekday(year, time.February, time.Monday, 3),
		"memorial_day":           lastWeekday(year, time.May, time.Monday),
		"juneteenth":             time.Date(year, time.June, 19, 0, 0, 0, 0, time.UTC),
		"independence_day":       time.Date(year, time.July, 4, 0, 0, 0, 0, time.UTC),
		"labor_day":              nthWeekday(year, time.September, time.Monday, 1),
		"columbus_day":           nthWeekday(year, time.October, time.Monday, 2),
		"veterans_day":           time.Date(year, time.November, 11, 0, 0, 0, 0, time.UTC),
		"thanksgiving_day":       nthWeekday(year, time.November, time.Thursday, 4),
		"christmas_day":          time.Date(year, time.December, 25, 0, 0, 0, 0, time.UTC),
	}
}

func nthWeekday(year int, month time.Month, weekday time.Weekday, occurrence int) time.Time {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	offset := (int(weekday) - int(first.Weekday()) + 7) % 7
	return first.AddDate(0, 0, offset+7*(occurrence-1))
}

func lastWeekday(year int, month time.Month, weekday time.Weekday) time.Time {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC)
	offset := (int(last.Weekday()) - int(weekday) + 7) % 7
	return last.AddDate(0, 0, -offset)
}

func observedFederalHoliday(actual time.Time) time.Time {
	switch actual.Weekday() {
	case time.Saturday:
		return actual.AddDate(0, 0, -1)
	case time.Sunday:
		return actual.AddDate(0, 0, 1)
	default:
		return actual
	}
}

func dateOnly(date time.Time) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
}
