package report

import (
	"path/filepath"
	"testing"
)

func TestScaleRowsRoundTripAndMerge(t *testing.T) {
	dir := t.TempDir()
	path := ScaleRowsPath(dir, "gcp")
	if want := filepath.Join(dir, "scale", "gcp.jsonl"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}

	// Nothing stored yet must not be an error.
	rows, err := LoadScaleRows(path)
	if err != nil || rows != nil {
		t.Fatalf("empty load = %v, %v", rows, err)
	}

	initial := []ScaleRow{
		{Date: "2026-01-05", Group: "job-a", Item: "cpu", Cost: 10},
		{Date: "2026-02-05", Group: "job-a", Item: "cpu", Cost: 20},
		{Date: "2026-03-05", Group: "job-a", Item: "cpu", Cost: 30},
	}
	if err := SaveScaleRows(path, initial); err != nil {
		t.Fatal(err)
	}
	got, err := LoadScaleRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Date != "2026-01-05" || got[2].Cost != 30 {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	// A run covering February only replaces February, keeping the backfill.
	fresh := []ScaleRow{{Date: "2026-02-05", Group: "job-a", Item: "cpu", Cost: 25}}
	merged := MergeScaleRows(got, fresh, "2026-02-01", "2026-03-01")
	var total float64
	for _, r := range merged {
		total += r.Cost
	}
	if len(merged) != 3 {
		t.Fatalf("merged rows = %d, want 3: %+v", len(merged), merged)
	}
	if total != 65 { // 10 + 25 + 30
		t.Errorf("merged total = %v, want 65", total)
	}
	if lo, hi := ScaleRowsRange(merged); lo != "2026-01-05" || hi != "2026-03-05" {
		t.Errorf("range = %s..%s, want 2026-01-05..2026-03-05", lo, hi)
	}
}

func TestCompactScaleRows(t *testing.T) {
	rows := []ScaleRow{
		{Date: "2026-07-01", Group: "g", Item: "big", Cost: 100},
		{Date: "2026-07-01", Group: "g", Item: "mid", Cost: 10},
		{Date: "2026-07-01", Group: "g", Item: "tail1", Cost: 1},
		{Date: "2026-07-01", Group: "g", Item: "tail2", Cost: 2},
		{Date: "2026-07-01", Group: "g", Item: "zero", Cost: 0},
		{Date: "2026-07-02", Group: "g", Item: "big", Cost: 5},
	}
	got := CompactScaleRows(rows, 2)

	var total float64
	byItem := map[string]float64{}
	for _, r := range got {
		total += r.Cost
		if r.Date == "2026-07-01" {
			byItem[r.Item] += r.Cost
		}
	}
	if total != 118 {
		t.Errorf("total = %v, want 118 (compaction must preserve cost)", total)
	}
	if byItem["big"] != 100 || byItem["mid"] != 10 {
		t.Errorf("top items not kept: %+v", byItem)
	}
	if byItem["Other"] != 3 { // tail1 + tail2
		t.Errorf("Other = %v, want 3", byItem["Other"])
	}
	if _, ok := byItem["zero"]; ok {
		t.Error("zero-cost rows must be dropped")
	}
}

