package calc

import (
	"math"
	"testing"
	"time"

	"github.com/kubernetes/k8s-cost/internal/model"
)

func day(s string) model.Date {
	d, err := model.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

func spend(date string, amount float64) model.DailySpend {
	return model.DailySpend{Provider: model.ProviderAWS, Date: day(date), Amount: amount, Currency: "USD"}
}

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.01 {
		t.Errorf("%s = %.4f, want %.4f", name, got, want)
	}
}

func TestCompute(t *testing.T) {
	// April 2026 (prev month, 30 days): $30/day => $900 total.
	// May 2026 (current month, 31 days): $100/day for first 10 days => $1000.
	var records []model.DailySpend
	for d := 1; d <= 30; d++ {
		records = append(records, spend(dateStr(2026, 4, d), 30))
	}
	// Jan-Mar constant $10/day to build YTD history.
	for m := 1; m <= 3; m++ {
		for d := 1; d <= daysInMonth(2026, time.Month(m)); d++ {
			records = append(records, spend(dateStr(2026, m, d), 10))
		}
	}
	for d := 1; d <= 10; d++ {
		records = append(records, spend(dateStr(2026, 5, d), 100))
	}

	asOf := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	budget := &model.BudgetConfig{Provider: model.ProviderAWS, Year: 2026, AnnualBudget: 6000, Currency: "USD", AlertThreshold: 0.9}

	m := Compute(model.ProviderAWS, records, asOf, budget)

	approx(t, "MonthlySpend", m.MonthlySpend, 1000)
	approx(t, "LastMonthAvgDaily", m.LastMonthAvgDaily, 30) // 900/30
	approx(t, "CurrentMonthAvgDaily", m.CurrentMonthAvgDaily, 100) // 1000/10
	// Projected full month = 100/day * 31 days; yearly = that * 12.
	approx(t, "MonthlyProjected", m.MonthlyProjected, 3100)
	approx(t, "YearlyBasedOnThisMonth", m.YearlyBasedOnThisMonth, 37200)

	// YTD = Jan(310) + Feb(280) + Mar(310) + Apr(900) + May(1000)
	// Jan 31*10=310, Feb 28*10=280, Mar 31*10=310 => 900 + 900 + 1000 = 2800
	approx(t, "YTD", m.YTD, 2800)

	// dailyAvgOverOneYear = (1000/31)*12/365
	approx(t, "DailyAvgOverOneYear", m.DailyAvgOverOneYear, (1000.0/31.0)*12/365)

	if m.Budget == nil {
		t.Fatal("expected budget metrics")
	}
	// Year-end projection uses the YTD run rate (robust for partial months):
	//   projectedYearTotal = ytd + (ytd/daysElapsedInYear) * remainingDaysInYear
	doy := asOf.YearDay()
	eoy := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	remDays := int(eoy.Sub(asOf).Hours() / 24)
	expProj := 2800.0 + (2800.0/float64(doy))*float64(remDays)
	approx(t, "BurnRate31Dec", m.BurnRate31Dec, expProj)
	approx(t, "ProjectedYearTotal", m.Budget.ProjectedYearTotal, expProj)
	approx(t, "BudgetUtilization", m.Budget.BudgetUtilization, expProj/6000.0)
	if !m.Budget.BudgetAlert {
		t.Error("expected budget alert to fire (utilization > 0.9)")
	}
}

func dateStr(y, m, d int) string {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

