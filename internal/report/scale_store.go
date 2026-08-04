package report

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The scale tab keeps its raw (day, group, item, cost) rows in the file-based
// store so a collection run only has to fetch *new* days: the rows for the
// queried window are replaced, everything older is kept. Rebuilding the whole
// year on every run would rescan months of BigQuery data for nothing.
//
// Rows are stored as JSON Lines with a compact array per record —
//
//	["2026-07-01","ci-kubernetes-e2e-gce-scale-performance-5000","E2 Instance Core running in Americas",123.4567,169.5]
//
// — which keeps the file small and produces readable, line-based git diffs.
// The trailing gross (pre-discount) amount is optional: files written before it
// existed still load, and gross then defaults to the net cost.

// ScaleRowsPath returns the store path holding one cloud's raw scale rows.
func ScaleRowsPath(dataDir, cloud string) string {
	return filepath.Join(dataDir, "scale", cloud+".jsonl")
}

// LoadScaleRows reads previously collected rows. A missing file is not an error
// (first run).
func LoadScaleRows(path string) ([]ScaleRow, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []ScaleRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var rec []json.RawMessage
		if err := json.Unmarshal([]byte(text), &rec); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if len(rec) < 4 {
			return nil, fmt.Errorf("%s:%d: expected at least 4 fields, got %d", path, line, len(rec))
		}
		var r ScaleRow
		if err := json.Unmarshal(rec[0], &r.Date); err != nil {
			return nil, fmt.Errorf("%s:%d date: %w", path, line, err)
		}
		if err := json.Unmarshal(rec[1], &r.Group); err != nil {
			return nil, fmt.Errorf("%s:%d group: %w", path, line, err)
		}
		if err := json.Unmarshal(rec[2], &r.Item); err != nil {
			return nil, fmt.Errorf("%s:%d item: %w", path, line, err)
		}
		if err := json.Unmarshal(rec[3], &r.Cost); err != nil {
			return nil, fmt.Errorf("%s:%d cost: %w", path, line, err)
		}
		r.Gross = r.Cost // pre-gross files: assume no discount
		if len(rec) > 4 {
			if err := json.Unmarshal(rec[4], &r.Gross); err != nil {
				return nil, fmt.Errorf("%s:%d gross: %w", path, line, err)
			}
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// SaveScaleRows writes rows sorted by (date, group, item) so the file stays
// stable across runs and diffs show only genuinely changed days.
func SaveScaleRows(path string, rows []ScaleRow) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	sorted := append([]ScaleRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return a.Item < b.Item
	})

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, r := range sorted {
		rec, err := json.Marshal([]any{r.Date, r.Group, r.Item, round4(r.Cost), round4(r.Gross)})
		if err != nil {
			return err
		}
		if _, err := w.Write(append(rec, '\n')); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Close()
}

// MergeScaleRows replaces the [start, end) window of existing with fresh.
// Costs for a day can still change days later (late-landing usage and
// credits), so the window is replaced wholesale rather than appended to.
func MergeScaleRows(existing, fresh []ScaleRow, start, end string) []ScaleRow {
	out := make([]ScaleRow, 0, len(existing)+len(fresh))
	for _, r := range existing {
		if r.Date >= start && r.Date < end {
			continue // superseded by the fresh window
		}
		out = append(out, r)
	}
	return append(out, fresh...)
}

// ScaleRowsRange returns the first and last date present, so the dashboard can
// label the covered window instead of the last collection window.
func ScaleRowsRange(rows []ScaleRow) (string, string) {
	if len(rows) == 0 {
		return "", ""
	}
	min, max := rows[0].Date, rows[0].Date
	for _, r := range rows {
		if r.Date < min {
			min = r.Date
		}
		if r.Date > max {
			max = r.Date
		}
	}
	return min, max
}

// CompactScaleRows shrinks a freshly fetched window before it enters the store:
// zero-cost rows are dropped and, per (day, group), only the biggest itemsPerDay
// resources are kept individually — the long tail of sub-cent SKUs is folded
// into "Other". Totals and daily series stay exact; only the resource split
// loses its (irrelevant) tail. Without this, the per-SKU daily detail of ~30
// projects is tens of megabytes of committed data.
func CompactScaleRows(rows []ScaleRow, itemsPerDay int) []ScaleRow {
	type key struct{ date, group string }
	type amounts struct{ cost, gross float64 }
	buckets := map[key]map[string]amounts{}
	order := make([]key, 0, len(rows))
	for _, r := range rows {
		k := key{r.Date, r.Group}
		if _, ok := buckets[k]; !ok {
			buckets[k] = map[string]amounts{}
			order = append(order, k)
		}
		a := buckets[k][r.Item]
		a.cost += r.Cost
		a.gross += r.Gross
		buckets[k][r.Item] = a
	}

	out := make([]ScaleRow, 0, len(order)*(itemsPerDay+1))
	for _, k := range order {
		type item struct {
			name string
			amounts
		}
		items := make([]item, 0, len(buckets[k]))
		for name, a := range buckets[k] {
			if round4(a.cost) != 0 || round4(a.gross) != 0 {
				items = append(items, item{name, a})
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].cost > items[j].cost })

		var other amounts
		for i, it := range items {
			if itemsPerDay > 0 && i >= itemsPerDay {
				other.cost += it.cost
				other.gross += it.gross
				continue
			}
			out = append(out, ScaleRow{
				Date: k.date, Group: k.group, Item: it.name,
				Cost: round4(it.cost), Gross: round4(it.gross),
			})
		}
		if round4(other.cost) != 0 || round4(other.gross) != 0 {
			out = append(out, ScaleRow{
				Date: k.date, Group: k.group, Item: "Other",
				Cost: round4(other.cost), Gross: round4(other.gross),
			})
		}
	}
	return out
}





