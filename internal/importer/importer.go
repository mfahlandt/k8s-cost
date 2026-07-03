// Package importer parses provider billing exports into normalized
// model.DailySpend records.
//
// Each provider exposes its own quirks (column names, date granularity,
// currency handling), so importers are built from a small, declarative
// CSVProfile plus provider-specific glue.
package importer

import (
	"fmt"
	"io"

	"github.com/kubernetes/k8s-cost/internal/model"
)

// Options tune an import run.
type Options struct {
	// DefaultCurrency is used when the source has no currency column.
	DefaultCurrency string
	// PeriodMonth, when set (non-zero), dates rows that lack a per-row date to
	// the first day of this month. Format: "2006-01". Used for month-aggregated
	// exports such as the GCP BigQuery service breakdown.
	PeriodMonth string
}

// Importer turns a raw export stream into normalized daily spend records.
type Importer interface {
	Provider() model.Provider
	Parse(r io.Reader, opts Options) ([]model.DailySpend, error)
}

// registry maps a "format" key to an importer factory.
var registry = map[string]Importer{}

func register(key string, imp Importer) { registry[key] = imp }

// Get returns the importer registered under key (e.g. "aws-csv").
func Get(key string) (Importer, error) {
	imp, ok := registry[key]
	if !ok {
		return nil, fmt.Errorf("unknown import format %q (available: %v)", key, Formats())
	}
	return imp, nil
}

// Formats lists registered format keys.
func Formats() []string {
	keys := make([]string, 0, len(registry))
	for k := range registry {
		keys = append(keys, k)
	}
	return keys
}

