// Package calc computes per-provider spend metrics and budget projections from
// normalized daily spend records. All formulas mirror the report spec.
package calc

import (
	"sort"
	"time"

	"github.com/kubernetes/k8s-cost/internal/model"
)

// Metrics is the full set of computed values for one provider as of a given
// reference date ("asOf"). Money values are in the provider currency.
type Metrics struct {
	Provider model.Provider `json:"provider"`
	AsOf     model.Date     `json:"asOf"`
	Currency string         `json:"currency"`

	// Core report figures.
	LastMonthAvgDaily      float64 `json:"lastMonthAvgDaily"`
	MonthlySpend           float64 `json:"monthlySpend"` // month-to-date for the current month
	MonthlyProjected       float64 `json:"monthlyProjected"` // projected full-month total (avgDaily * daysInMonth)
	YearlyBasedOnThisMonth float64 `json:"yearlyBasedOnThisMonth"`
	YTD                    float64 `json:"ytd"`
	BurnRate31Dec          float64 `json:"burnRate31Dec"`
	DailyAvgOverOneYear    float64 `json:"dailyAvgOverOneYear"`

	// Helper values, surfaced for the dashboard.
	CurrentMonthAvgDaily float64 `json:"currentMonthAvgDaily"`
	DaysElapsedInMonth   int     `json:"daysElapsedInMonth"`
	DaysInCurrentMonth   int     `json:"daysInCurrentMonth"`
	DaysRemainingInYear  int     `json:"daysRemainingInYear"`

	// Budget projection (populated only when a budget is configured).
	Budget *BudgetMetrics `json:"budget,omitempty"`

	// Series is the month-by-month progression for the reference year (Jan..Dec),
	// with per-month totals and cumulative YTD — for the dashboard charts.
	// Months after asOf are forecast from the YTD run rate (Forecast=true).
	Series []MonthPoint `json:"series"`

	// WeeklySeries is the Sat–Fri weekly progression (like the old sheet's
	// weekly tab). For daily-granular providers (AWS/GCP/Fastly) it uses real
	// daily data; for monthly-billed providers (DO/IBM) it is derived by
	// spreading each monthly invoice evenly across the month's days
	// (WeeklyDerived=true) — same approach as the old Excel.
	WeeklySeries  []WeekPoint `json:"weeklySeries,omitempty"`
	WeeklyDerived bool        `json:"weeklyDerived,omitempty"`
}

// MonthPoint is one month's spend and running cumulative total for the year.
type MonthPoint struct {
	Month      int     `json:"month"` // 1-12
	Monthly    float64 `json:"monthly"`
	Cumulative float64 `json:"cumulative"`
	Forecast   bool    `json:"forecast,omitempty"` // projected, not actual
}

// WeekPoint is one Sat–Fri week's spend and running cumulative for the year.
type WeekPoint struct {
	WeekEnd    model.Date `json:"weekEnd"` // the Friday ending the week
	Weekly     float64    `json:"weekly"`
	Cumulative float64    `json:"cumulative"`
}

// BudgetMetrics captures the budget-tracking and alerting figures.
type BudgetMetrics struct {
	AnnualBudget               float64 `json:"annualBudget"`
	AlertThreshold             float64 `json:"alertThreshold"`
	ProjectedYearTotal         float64 `json:"projectedYearTotal"`
	BudgetUtilization          float64 `json:"budgetUtilization"`
	BudgetAlert                bool    `json:"budgetAlert"`
	MonthsUntilBudgetExhausted float64 `json:"monthsUntilBudgetExhausted"`
	Remaining                  float64 `json:"remaining"`
	// DaysUntilBudgetExhausted is how many more days the remaining budget
	// (annualBudget - ytd) lasts at the current YTD daily burn rate. Negative
	// means the budget is already spent.
	DaysUntilBudgetExhausted float64 `json:"daysUntilBudgetExhausted"`
	// BudgetExhaustedDate is asOf + DaysUntilBudgetExhausted ("day zero").
	// Empty when the burn rate is zero.
	BudgetExhaustedDate string `json:"budgetExhaustedDate,omitempty"`
}

// daysInMonth returns the number of days in the month containing t.
func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// Compute derives all metrics for a provider from its daily spend records,
// evaluated as of the given date. budget may be nil.
func Compute(provider model.Provider, records []model.DailySpend, asOf time.Time, budget *model.BudgetConfig) Metrics {
	asOf = time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, time.UTC)
	year := asOf.Year()
	month := asOf.Month()

	prevMonthTime := asOf.AddDate(0, -1, 0)
	prevYear, prevMonth := prevMonthTime.Year(), prevMonthTime.Month()

	daysInCur := daysInMonth(year, month)
	daysInPrev := daysInMonth(prevYear, prevMonth)
	daysElapsed := asOf.Day() // days of current month elapsed through asOf

	var monthlySpend, prevMonthTotal, ytd float64
	daysWithData := map[int]struct{}{} // distinct current-month days that have records
	var monthlyByMonth [12]float64     // per-month totals for the reference year
	weeklyTotals := map[time.Time]float64{} // Sat–Fri weeks, keyed by ending Friday
	dailyGranular := false                  // true if any record is not on the 1st
	for _, r := range records {
		d := r.Date.Time
		if d.After(asOf) {
			continue
		}
		if d.Year() == year && d.Month() == month {
			monthlySpend += r.Amount
			daysWithData[d.Day()] = struct{}{}
		}
		if d.Year() == prevYear && d.Month() == prevMonth {
			prevMonthTotal += r.Amount
		}
		if d.Year() == year {
			ytd += r.Amount
			monthlyByMonth[d.Month()-1] += r.Amount
			weeklyTotals[weekEndFriday(d)] += r.Amount
			if d.Day() != 1 {
				dailyGranular = true
			}
		}
	}
	// Divide the current-month average by the days that actually have data, not
	// raw calendar days elapsed. This avoids understating the run rate when the
	// as-of date is ahead of the latest ingested billing day (GCP costs land a
	// few hours/days late).
	dataDays := len(daysWithData)
	if dataDays == 0 {
		dataDays = daysElapsed
	}

	currency := ""
	if len(records) > 0 {
		currency = records[0].Currency
	}
	if budget != nil && budget.Currency != "" {
		currency = budget.Currency
	}

	m := Metrics{
		Provider:           provider,
		AsOf:               model.NewDate(asOf),
		Currency:           currency,
		MonthlySpend:       monthlySpend,
		YTD:                ytd,
		DaysElapsedInMonth: dataDays,
		DaysInCurrentMonth: daysInCur,
	}

	if daysInPrev > 0 {
		m.LastMonthAvgDaily = prevMonthTotal / float64(daysInPrev)
	}

	if dataDays > 0 {
		m.CurrentMonthAvgDaily = monthlySpend / float64(dataDays)
	}

	// Project the (possibly partial) current month to a full-month figure, and
	// base the "yearly from this month" number on it. Using the raw MTD total
	// would wildly understate both early in a month.
	m.MonthlyProjected = m.CurrentMonthAvgDaily * float64(daysInCur)
	m.YearlyBasedOnThisMonth = m.MonthlyProjected * 12

	// burnRate31Dec projects the year-end total from the YTD run rate:
	//   ytd + (ytd / daysElapsedInYear) * remainingDaysInYear
	// This mirrors the sheet's "Total spend on 31 Dec based on YTD avg" and is
	// robust for both daily-granular (AWS/GCP) and monthly-billed (DO/IBM)
	// providers, and for partially-elapsed current months. Extrapolating the
	// current (possibly partial) month's daily rate would wildly over/under-shoot.
	endOfYear := time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC)
	remainingDays := int(endOfYear.Sub(asOf).Hours() / 24)
	daysElapsedInYear := asOf.YearDay()
	ytdDailyAvg := 0.0
	if daysElapsedInYear > 0 {
		ytdDailyAvg = ytd / float64(daysElapsedInYear)
	}
	m.BurnRate31Dec = ytd + ytdDailyAvg*float64(remainingDays)
	m.DaysRemainingInYear = remainingDays

	// dailyAvgOverOneYear = (monthlySpend / daysInCurrentMonth) * 12 / 365
	if daysInCur > 0 {
		m.DailyAvgOverOneYear = (monthlySpend / float64(daysInCur)) * 12 / 365
	}

	if budget != nil && budget.AnnualBudget > 0 {
		m.Budget = computeBudget(*budget, ytd, ytdDailyAvg, m.BurnRate31Dec, daysElapsedInYear, asOf)
	}

	// Build the month-by-month series: actuals through the current month (the
	// current month additionally gets its remaining days projected, so the
	// December cumulative matches BurnRate31Dec), then forecast months from the
	// YTD daily run rate.
	cum := 0.0
	for i := 0; i < 12; i++ {
		mp := MonthPoint{Month: i + 1}
		switch {
		case i < int(month)-1:
			mp.Monthly = monthlyByMonth[i]
		case i == int(month)-1:
			remainderDays := daysInCur - asOf.Day()
			mp.Monthly = monthlyByMonth[i] + ytdDailyAvg*float64(remainderDays)
			mp.Forecast = remainderDays > 0 // partially projected
		default:
			mp.Monthly = ytdDailyAvg * float64(daysInMonth(year, time.Month(i+1)))
			mp.Forecast = true
		}
		cum += mp.Monthly
		mp.Cumulative = cum
		m.Series = append(m.Series, mp)
	}

	// Weekly (Sat–Fri) series like the old sheet's weekly tab. Monthly-billed
	// providers get derived weeks: invoice spread evenly across the month.
	if !dailyGranular {
		weeklyTotals = map[time.Time]float64{}
		for mo := 1; mo <= int(month); mo++ {
			total := monthlyByMonth[mo-1]
			if total == 0 {
				continue
			}
			dim := daysInMonth(year, time.Month(mo))
			perDay := total / float64(dim)
			for day := 1; day <= dim; day++ {
				d := time.Date(year, time.Month(mo), day, 0, 0, 0, 0, time.UTC)
				if d.After(asOf) {
					break
				}
				weeklyTotals[weekEndFriday(d)] += perDay
			}
		}
		m.WeeklyDerived = true
	}
	if len(weeklyTotals) > 0 {
		ends := make([]time.Time, 0, len(weeklyTotals))
		for e := range weeklyTotals {
			ends = append(ends, e)
		}
		sort.Slice(ends, func(i, j int) bool { return ends[i].Before(ends[j]) })
		wcum := 0.0
		for _, e := range ends {
			wcum += weeklyTotals[e]
			m.WeeklySeries = append(m.WeeklySeries, WeekPoint{
				WeekEnd:    model.NewDate(e),
				Weekly:     weeklyTotals[e],
				Cumulative: wcum,
			})
		}
	}
	return m
}

// weekEndFriday returns the Friday on or after d (Sat–Fri weeks, per the old
// sheet's "run billing report for range of Sat-Fri" note).
func weekEndFriday(d time.Time) time.Time {
	offset := (int(time.Friday) - int(d.Weekday()) + 7) % 7
	return d.AddDate(0, 0, offset)
}

// computeBudget derives budget utilization and runway from the YTD run rate.
// projectedYearTotal equals burnRate31Dec (the YTD-average year-end projection).
func computeBudget(cfg model.BudgetConfig, ytd, ytdDailyAvg, projectedYearTotal float64, daysElapsedInYear int, asOf time.Time) *BudgetMetrics {
	util := 0.0
	if cfg.AnnualBudget > 0 {
		util = projectedYearTotal / cfg.AnnualBudget
	}

	// Runway until the annual budget is exhausted at the current run rate.
	const avgDaysPerMonth = 30.4375
	monthlyRunRate := ytdDailyAvg * avgDaysPerMonth
	monthsElapsed := float64(daysElapsedInYear) / avgDaysPerMonth
	monthsUntilExhausted := 0.0
	if monthlyRunRate > 0 {
		monthsUntilExhausted = cfg.AnnualBudget/monthlyRunRate - monthsElapsed
	}

	daysUntilExhausted := 0.0
	exhaustedDate := ""
	if ytdDailyAvg > 0 {
		daysUntilExhausted = (cfg.AnnualBudget - ytd) / ytdDailyAvg
		exhaustedDate = asOf.AddDate(0, 0, int(daysUntilExhausted)).Format("2006-01-02")
	}

	return &BudgetMetrics{
		AnnualBudget:               cfg.AnnualBudget,
		AlertThreshold:             cfg.AlertThreshold,
		ProjectedYearTotal:         projectedYearTotal,
		BudgetUtilization:          util,
		BudgetAlert:                cfg.AlertThreshold > 0 && util >= cfg.AlertThreshold,
		MonthsUntilBudgetExhausted: monthsUntilExhausted,
		Remaining:                  cfg.AnnualBudget - projectedYearTotal,
		DaysUntilBudgetExhausted:   daysUntilExhausted,
		BudgetExhaustedDate:        exhaustedDate,
	}
}





