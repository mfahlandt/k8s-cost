// Package store implements a file-based, diff-friendly persistence layer.
//
// Layout on disk (rooted at the store directory, typically ./data):
//
//	data/
//	  spend/<provider>.json    -> []model.DailySpend (sorted by date, then service)
//	  budgets/<provider>.json  -> model.BudgetConfig
//
// The JSON files are intended to be committed to git so the whole dataset
// lives in the repository and can be published via GitHub Pages.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kubernetes/k8s-cost/internal/model"
)

// Store persists normalized billing data as JSON files on disk.
type Store struct {
	root string
}

// New returns a Store rooted at dir, creating the directory layout if needed.
func New(dir string) (*Store, error) {
	for _, sub := range []string{"spend", "budgets"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("create store dir: %w", err)
		}
	}
	return &Store{root: dir}, nil
}

func (s *Store) spendPath(p model.Provider) string {
	return filepath.Join(s.root, "spend", string(p)+".json")
}

func (s *Store) budgetPath(p model.Provider) string {
	return filepath.Join(s.root, "budgets", string(p)+".json")
}

// LoadSpend returns all daily spend records for a provider. A missing file is
// treated as an empty dataset.
func (s *Store) LoadSpend(p model.Provider) ([]model.DailySpend, error) {
	var out []model.DailySpend
	if err := readJSON(s.spendPath(p), &out); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return out, nil
}

// SaveSpend writes the given records, sorted deterministically for stable diffs.
func (s *Store) SaveSpend(p model.Provider, records []model.DailySpend) error {
	sort.SliceStable(records, func(i, j int) bool {
		if !records[i].Date.Equal(records[j].Date.Time) {
			return records[i].Date.Before(records[j].Date.Time)
		}
		return records[i].Service < records[j].Service
	})
	return writeJSON(s.spendPath(p), records)
}

// MergeSpend upserts records into the existing dataset. Records are keyed by
// (date, service); incoming records replace existing ones with the same key so
// re-importing a period is idempotent.
func (s *Store) MergeSpend(p model.Provider, incoming []model.DailySpend) (added, updated int, err error) {
	existing, err := s.LoadSpend(p)
	if err != nil {
		return 0, 0, err
	}
	type key struct {
		date    string
		service string
	}
	index := make(map[key]int, len(existing))
	for i, r := range existing {
		index[key{r.Date.String(), r.Service}] = i
	}
	for _, r := range incoming {
		k := key{r.Date.String(), r.Service}
		if i, ok := index[k]; ok {
			existing[i] = r
			updated++
		} else {
			index[k] = len(existing)
			existing = append(existing, r)
			added++
		}
	}
	if err := s.SaveSpend(p, existing); err != nil {
		return 0, 0, err
	}
	return added, updated, nil
}

// ReplaceSpendRange replaces every record whose date falls in [start, end) with
// the incoming set, leaving records outside the window untouched. Records
// outside the window are kept; incoming records are assumed to already lie
// within it.
//
// API collectors that return a complete picture of a date range (AWS Cost
// Explorer daily, GCP BigQuery daily) use this instead of MergeSpend so that
// (a) re-collecting a window is idempotent, and (b) switching a window from
// day-total records (service == "") to per-service records never double-counts
// — the old whole-day rows in the window are dropped before the new per-service
// rows are inserted, keeping the per-day totals exact.
func (s *Store) ReplaceSpendRange(p model.Provider, start, end time.Time, incoming []model.DailySpend) (removed, added int, err error) {
	existing, err := s.LoadSpend(p)
	if err != nil {
		return 0, 0, err
	}
	kept := make([]model.DailySpend, 0, len(existing))
	for _, r := range existing {
		d := r.Date.Time
		if !d.Before(start) && d.Before(end) {
			removed++
			continue // inside the window: replaced by incoming
		}
		kept = append(kept, r)
	}
	kept = append(kept, incoming...)
	added = len(incoming)
	if err := s.SaveSpend(p, kept); err != nil {
		return 0, 0, err
	}
	return removed, added, nil
}

// LoadBudgets returns all year-scoped budget configs for a provider, sorted by
// year. It tolerates the legacy single-object format (one budget per file).
func (s *Store) LoadBudgets(p model.Provider) ([]model.BudgetConfig, error) {
	b, err := os.ReadFile(s.budgetPath(p))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var list []model.BudgetConfig
	if err := json.Unmarshal(b, &list); err != nil {
		// Legacy: a single BudgetConfig object.
		var one model.BudgetConfig
		if err2 := json.Unmarshal(b, &one); err2 != nil {
			return nil, err
		}
		list = []model.BudgetConfig{one}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Year < list[j].Year })
	return list, nil
}

// LoadBudget returns the budget config for a provider in the given year, or
// (nil, nil) if none is configured for that year.
func (s *Store) LoadBudget(p model.Provider, year int) (*model.BudgetConfig, error) {
	list, err := s.LoadBudgets(p)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Year == year {
			return &list[i], nil
		}
	}
	return nil, nil
}

// SaveBudget upserts the budget config for a provider/year, preserving budgets
// for other years so each year keeps its own budget (they reset on Jan 1).
func (s *Store) SaveBudget(cfg model.BudgetConfig) error {
	list, err := s.LoadBudgets(cfg.Provider)
	if err != nil {
		return err
	}
	replaced := false
	for i := range list {
		if list[i].Year == cfg.Year {
			list[i] = cfg
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, cfg)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Year < list[j].Year })
	return writeJSON(s.budgetPath(cfg.Provider), list)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}


