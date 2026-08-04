package report

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"
)

// Scale is the JSON document behind the dashboard's "Scale tests" tab. It is
// produced by `costctl collect-scale` and committed next to dashboard.json so
// GitHub Pages can serve it statically.
//
// The two clouds answer the same question with different granularity:
//   - GCP attributes cost per prow job (the billing export carries the
//     `prow_k8s_io_job` resource label), so groups are jobs.
//   - AWS has no activated `prow.k8s.io/job` cost allocation tag, so groups are
//     the dedicated scale boskos accounts instead.
type Scale struct {
	GeneratedAt time.Time  `json:"generatedAt"`
	Start       string     `json:"start"`
	End         string     `json:"end"`
	Clouds      []ScaleSet `json:"clouds"`
}

// ScaleSet is one cloud's scale-test spend.
type ScaleSet struct {
	Cloud     string       `json:"cloud"`     // "gcp" | "aws"
	GroupKind string       `json:"groupKind"` // "job" | "account"
	Note      string       `json:"note"`
	Currency  string       `json:"currency"`
	Total     float64      `json:"total"`
	Groups    []ScaleGroup `json:"groups"`
	Breakdown []ScaleItem  `json:"breakdown"` // resource split across all groups
	Daily     []ScalePoint `json:"daily"`
}

// ScaleGroup is one prow job (GCP) or one account (AWS).
type ScaleGroup struct {
	Name string  `json:"name"`
	Cost float64 `json:"cost"`
	// ActiveDays counts days with non-trivial spend — for periodic jobs this
	// equals the number of runs, hence CostPerActiveDay ≈ cost per run.
	ActiveDays       int          `json:"activeDays"`
	CostPerActiveDay float64      `json:"costPerActiveDay"`
	Breakdown        []ScaleItem  `json:"breakdown"`
	Daily            []ScalePoint `json:"daily"`
}

// ScaleItem is one resource bucket (GCP SKU / AWS service or usage type).
type ScaleItem struct {
	Name string  `json:"name"`
	Cost float64 `json:"cost"`
}

// ScalePoint is one day of spend.
type ScalePoint struct {
	Date string  `json:"date"`
	Cost float64 `json:"cost"`
}

// activeDayThreshold: a day counts as "active" once it carries at least 5% of
// the group's busiest day, which filters out leftover disks/IPs lingering after
// a run without dropping real (cheaper) runs.
const activeDayThreshold = 0.05

// BuildScaleSet aggregates (day, group, resource, cost) tuples into a ScaleSet.
// Callers pass raw rows; ranking, daily series and per-run figures are derived
// here so both collectors stay dumb.
func BuildScaleSet(cloud, groupKind, note, currency string, rows []ScaleRow, topN int) ScaleSet {
	set := ScaleSet{Cloud: cloud, GroupKind: groupKind, Note: note, Currency: currency}

	groups := map[string]*ScaleGroup{}
	groupItems := map[string]map[string]float64{}
	groupDays := map[string]map[string]float64{}
	items := map[string]float64{}
	days := map[string]float64{}

	for _, r := range rows {
		if r.Group == "" {
			r.Group = "(unlabelled)"
		}
		g, ok := groups[r.Group]
		if !ok {
			g = &ScaleGroup{Name: r.Group}
			groups[r.Group] = g
			groupItems[r.Group] = map[string]float64{}
			groupDays[r.Group] = map[string]float64{}
		}
		g.Cost += r.Cost
		groupItems[r.Group][r.Item] += r.Cost
		groupDays[r.Group][r.Date] += r.Cost
		items[r.Item] += r.Cost
		days[r.Date] += r.Cost
		set.Total += r.Cost
	}

	for name, g := range groups {
		g.Breakdown = topItems(groupItems[name], topN)
		g.Daily = sortedPoints(groupDays[name])
		var max float64
		for _, p := range g.Daily {
			if p.Cost > max {
				max = p.Cost
			}
		}
		for _, p := range g.Daily {
			if p.Cost > max*activeDayThreshold {
				g.ActiveDays++
			}
		}
		g.Cost = round4(g.Cost)
		if g.ActiveDays > 0 {
			g.CostPerActiveDay = round4(g.Cost / float64(g.ActiveDays))
		}
		set.Groups = append(set.Groups, *g)
	}
	sort.Slice(set.Groups, func(i, j int) bool { return set.Groups[i].Cost > set.Groups[j].Cost })

	set.Total = round4(set.Total)
	set.Breakdown = topItems(items, topN)
	set.Daily = sortedPoints(days)
	return set
}

// round4 keeps the JSON small and readable; four decimals still resolves
// sub-cent SKUs while dropping float noise like 0.30000000000000004.
func round4(v float64) float64 {
	return math.Round(v*1e4) / 1e4
}

// ScaleRow is one (day, group, resource) cost tuple fed into BuildScaleSet.
type ScaleRow struct {
	Date  string
	Group string
	Item  string
	Cost  float64
}

// topItems ranks a resource map by cost and folds the tail into "Other (n)".
func topItems(m map[string]float64, topN int) []ScaleItem {
	out := make([]ScaleItem, 0, len(m))
	for k, v := range m {
		if k == "" {
			k = "(none)"
		}
		out = append(out, ScaleItem{Name: k, Cost: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cost > out[j].Cost })
	if topN > 0 && len(out) > topN {
		var rest float64
		for _, it := range out[topN:] {
			rest += it.Cost
		}
		out = append(out[:topN:topN], ScaleItem{Name: "Other", Cost: rest})
	}
	for i := range out {
		out[i].Cost = round4(out[i].Cost)
	}
	return out
}

// sortedPoints drops days without spend — the series is sparse anyway and this
// keeps the committed JSON small.
func sortedPoints(m map[string]float64) []ScalePoint {
	out := make([]ScalePoint, 0, len(m))
	for k, v := range m {
		if r := round4(v); r != 0 {
			out = append(out, ScalePoint{Date: k, Cost: r})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// WriteScaleJSON writes the scale-test document to path.
func WriteScaleJSON(path string, s Scale) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
