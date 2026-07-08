package importer

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/kubernetes/k8s-cost/internal/model"
)

// CSVProfile declaratively describes how to map a CSV export onto DailySpend.
//
// Column names are matched case-insensitively against the header row. Any of
// the listed aliases may be present; the first match wins.
type CSVProfile struct {
	Provider model.Provider

	// DateAliases are candidate header names for the per-row date column. If
	// none are present the importer falls back to Options.PeriodMonth.
	DateAliases []string
	// DateLayouts are tried in order when parsing a date cell.
	DateLayouts []string
	// AmountAliases are candidate header names for the cost/amount column.
	AmountAliases []string
	// ServiceAliases are candidate header names for the service column.
	ServiceAliases []string
	// CurrencyAliases are candidate header names for the currency column.
	CurrencyAliases []string

	// Aggregate, when true, sums rows that share the same (date, service) key
	// into a single record. Required for resource-granular exports (e.g. the
	// Azure usage CSV emits one row per resource per day) so the day/service
	// totals survive the store's (date, service)-keyed MergeSpend.
	Aggregate bool
}

// parseCSV is the shared engine used by every provider profile.
func parseCSV(r io.Reader, profile CSVProfile, opts Options) ([]model.DailySpend, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // tolerate ragged rows
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	idx := indexHeader(header)

	dateCol := firstIndex(idx, profile.DateAliases)
	amountCol := firstIndex(idx, profile.AmountAliases)
	serviceCol := firstIndex(idx, profile.ServiceAliases)
	currencyCol := firstIndex(idx, profile.CurrencyAliases)

	if amountCol < 0 {
		return nil, fmt.Errorf("no amount column found; looked for %v in %v", profile.AmountAliases, header)
	}

	var periodDate model.Date
	havePeriod := false
	if opts.PeriodMonth != "" {
		t, err := time.Parse("2006-01", opts.PeriodMonth)
		if err != nil {
			return nil, fmt.Errorf("invalid period month %q: %w", opts.PeriodMonth, err)
		}
		periodDate = model.NewDate(t)
		havePeriod = true
	}
	if dateCol < 0 && !havePeriod {
		return nil, fmt.Errorf("no date column found (looked for %v) and no --period provided", profile.DateAliases)
	}

	currency := opts.DefaultCurrency
	if currency == "" {
		currency = "USD"
	}

	var out []model.DailySpend
	line := 1
	for {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if isBlank(row) {
			continue
		}

		amount, ok := parseAmount(cell(row, amountCol))
		if !ok {
			continue // skip subtotal/blank rows we can't parse
		}

		date := periodDate
		if dateCol >= 0 {
			d, err := parseDate(cell(row, dateCol), profile.DateLayouts)
			if err != nil {
				if havePeriod {
					date = periodDate
				} else {
					return nil, fmt.Errorf("line %d: %w", line, err)
				}
			} else {
				date = d
			}
		}

		rowCurrency := currency
		if currencyCol >= 0 {
			if c := strings.TrimSpace(cell(row, currencyCol)); c != "" {
				rowCurrency = c
			}
		}

		out = append(out, model.DailySpend{
			Provider: profile.Provider,
			Date:     date,
			Amount:   amount,
			Currency: rowCurrency,
			Service:  strings.TrimSpace(cell(row, serviceCol)),
		})
	}
	if profile.Aggregate {
		out = aggregate(out)
	}
	return out, nil
}

// aggregate collapses records that share the same (date, service) key by
// summing their amounts, preserving the currency of the first record seen.
// Insertion order is preserved so the result stays deterministic before the
// store re-sorts it.
func aggregate(in []model.DailySpend) []model.DailySpend {
	type key struct {
		date    string
		service string
	}
	index := make(map[key]int, len(in))
	out := make([]model.DailySpend, 0, len(in))
	for _, r := range in {
		k := key{r.Date.String(), r.Service}
		if i, ok := index[k]; ok {
			out[i].Amount += r.Amount
			continue
		}
		index[k] = len(out)
		out = append(out, r)
	}
	return out
}

func indexHeader(header []string) map[string]int {
	m := make(map[string]int, len(header))
	for i, h := range header {
		m[normalizeHeader(h)] = i
	}
	return m
}

func normalizeHeader(s string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s, "\ufeff")))
}

func firstIndex(idx map[string]int, aliases []string) int {
	for _, a := range aliases {
		if i, ok := idx[normalizeHeader(a)]; ok {
			return i
		}
	}
	return -1
}

func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return row[i]
}

func isBlank(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func parseDate(s string, layouts []string) (model.Date, error) {
	s = strings.TrimSpace(s)
	// Handle timestamps like "2025-05-01 00:00:00 UTC" by taking the date part.
	if len(s) >= 10 {
		if d, err := model.ParseDate(s[:10]); err == nil {
			return d, nil
		}
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return model.NewDate(t), nil
		}
	}
	return model.Date{}, fmt.Errorf("cannot parse date %q", s)
}

// parseAmount handles "$1,234.56", "(123.45)" (negative), and plain numbers.
func parseAmount(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg = true
		s = s[1 : len(s)-1]
	}
	s = strings.NewReplacer("$", "", ",", "", "€", "", "£", "", " ", "").Replace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		v = -v
	}
	return v, true
}

