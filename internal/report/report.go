// Package report renders computed metrics into an XLSX workbook matching the
// per-provider report template, and into a JSON payload for the dashboard.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/kubernetes/k8s-cost/internal/calc"
	"github.com/kubernetes/k8s-cost/internal/model"
	"github.com/xuri/excelize/v2"
)

// Dashboard is the JSON document consumed by the React frontend.
type Dashboard struct {
	GeneratedAt time.Time      `json:"generatedAt"`
	AsOf        model.Date     `json:"asOf"`
	Providers   []calc.Metrics `json:"providers"`
	Totals      Totals         `json:"totals"`
	// Snapshots holds one entry per elapsed month of the year (as-of each
	// month's end; the current month is as-of the report date), so the UI can
	// switch between months.
	Snapshots []Snapshot `json:"snapshots,omitempty"`
}

// Snapshot is the dashboard state evaluated as of one month of the year.
type Snapshot struct {
	Label     string         `json:"label"` // e.g. "2026-03"
	AsOf      model.Date     `json:"asOf"`
	Providers []calc.Metrics `json:"providers"`
	Totals    Totals         `json:"totals"`
}

// Totals aggregates headline figures across all providers (USD-normalized
// naively by summing; mixed currencies are the caller's responsibility).
type Totals struct {
	MonthlySpend       float64 `json:"monthlySpend"`
	YTD                float64 `json:"ytd"`
	BurnRate31Dec      float64 `json:"burnRate31Dec"`
	ProjectedYearTotal float64 `json:"projectedYearTotal"`
	AnnualBudget       float64 `json:"annualBudget"`
	AlertCount         int     `json:"alertCount"`
}

// BuildDashboard assembles the dashboard document from per-provider metrics.
func BuildDashboard(asOf time.Time, metrics []calc.Metrics) Dashboard {
	return Dashboard{
		GeneratedAt: time.Now().UTC(),
		AsOf:        model.NewDate(asOf),
		Providers:   metrics,
		Totals:      buildTotals(metrics),
	}
}

// BuildSnapshot assembles one month's snapshot from per-provider metrics.
func BuildSnapshot(label string, asOf time.Time, metrics []calc.Metrics) Snapshot {
	return Snapshot{
		Label:     label,
		AsOf:      model.NewDate(asOf),
		Providers: metrics,
		Totals:    buildTotals(metrics),
	}
}

// buildTotals sums USD providers only (bandwidth-based providers like Fastly
// carry a non-currency unit and must not be added to dollar totals).
func buildTotals(metrics []calc.Metrics) Totals {
	var t Totals
	for _, m := range metrics {
		if m.Budget != nil && m.Budget.BudgetAlert {
			t.AlertCount++
		}
		if m.Currency != "" && m.Currency != "USD" {
			continue
		}
		t.MonthlySpend += m.MonthlySpend
		t.YTD += m.YTD
		t.BurnRate31Dec += m.BurnRate31Dec
		if m.Budget != nil {
			t.ProjectedYearTotal += m.Budget.ProjectedYearTotal
			t.AnnualBudget += m.Budget.AnnualBudget
		}
	}
	return t
}

// WriteJSON writes the dashboard document to path.
func WriteJSON(path string, d Dashboard) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// WriteXLSX renders the report workbook to path.
func WriteXLSX(path string, d Dashboard) error {
	f := excelize.NewFile()
	defer f.Close()

	const sheet = "Report"
	f.SetSheetName("Sheet1", sheet)

	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	money, _ := f.NewStyle(&excelize.Style{NumFmt: 44}) // accounting/currency
	pct, _ := f.NewStyle(&excelize.Style{NumFmt: 10})   // 0.00%

	row := 1
	set := func(col string, v any) {
		_ = f.SetCellValue(sheet, fmt.Sprintf("%s%d", col, row), v)
	}
	style := func(cells string, id int) { _ = f.SetCellStyle(sheet, cells, cells, id) }

	set("A", "Kubernetes Cloud Spend Report")
	style("A1", bold)
	row++
	set("A", "As of")
	set("B", d.AsOf.String())
	row += 2

	for _, m := range d.Providers {
		set("A", m.Provider.DisplayName())
		style(fmt.Sprintf("A%d", row), bold)
		row++

		lines := []struct {
			label string
			val   float64
		}{
			{"Last month's average daily spend", m.LastMonthAvgDaily},
			{"Monthly spend (month-to-date)", m.MonthlySpend},
			{"This month projected (full month)", m.MonthlyProjected},
			{"Yearly spend based on this month", m.YearlyBasedOnThisMonth},
			{"YTD", m.YTD},
			{"(burn rate) Total spend on 31 Dec", m.BurnRate31Dec},
			{"This month's daily average over one year", m.DailyAvgOverOneYear},
		}
		for _, l := range lines {
			set("A", l.label)
			set("B", l.val)
			style(fmt.Sprintf("B%d", row), money)
			row++
		}

		if m.Budget != nil {
			set("A", "Annual budget")
			set("B", m.Budget.AnnualBudget)
			style(fmt.Sprintf("B%d", row), money)
			row++
			set("A", "Projected year total")
			set("B", m.Budget.ProjectedYearTotal)
			style(fmt.Sprintf("B%d", row), money)
			row++
			set("A", "Budget utilization")
			set("B", m.Budget.BudgetUtilization)
			style(fmt.Sprintf("B%d", row), pct)
			row++
			set("A", "Budget alert")
			set("B", boolLabel(m.Budget.BudgetAlert))
			row++
			set("A", "Months until budget exhausted")
			set("B", m.Budget.MonthsUntilBudgetExhausted)
			row++
		}
		row++ // blank spacer between providers
	}

	_ = f.SetColWidth(sheet, "A", "A", 44)
	_ = f.SetColWidth(sheet, "B", "B", 18)
	return f.SaveAs(path)
}

func boolLabel(b bool) string {
	if b {
		return "YES"
	}
	return "no"
}

