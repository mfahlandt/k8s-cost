package report

import "testing"

func TestBuildScaleSet(t *testing.T) {
	rows := []ScaleRow{
		// job A: two busy days plus a trailing leftover day below the 5% cut.
		{Date: "2026-07-01", Group: "job-a", Item: "cpu", Cost: 100},
		{Date: "2026-07-01", Group: "job-a", Item: "ram", Cost: 50},
		{Date: "2026-07-03", Group: "job-a", Item: "cpu", Cost: 120},
		{Date: "2026-07-04", Group: "job-a", Item: "disk", Cost: 1},
		// job B: single day.
		{Date: "2026-07-02", Group: "job-b", Item: "cpu", Cost: 40},
	}
	set := BuildScaleSet("gcp", "job", "note", "USD", rows, 2)

	if got, want := set.Total, 311.0; got != want {
		t.Fatalf("total = %v, want %v", got, want)
	}
	if len(set.Groups) != 2 || set.Groups[0].Name != "job-a" {
		t.Fatalf("groups not ranked by cost: %+v", set.Groups)
	}
	a := set.Groups[0]
	if a.Cost != 271 {
		t.Errorf("job-a cost = %v, want 271", a.Cost)
	}
	// 2026-07-04 carries 1 of a 150 peak (<5%), so it must not count as a run.
	if a.ActiveDays != 2 {
		t.Errorf("job-a activeDays = %d, want 2", a.ActiveDays)
	}
	if got, want := a.CostPerActiveDay, 135.5; got != want {
		t.Errorf("job-a costPerActiveDay = %v, want %v", got, want)
	}
	if len(a.Daily) != 3 || a.Daily[0].Date != "2026-07-01" {
		t.Errorf("daily series wrong: %+v", a.Daily)
	}

	// topN = 2 folds the tail into a single "Other" bucket without losing money.
	if len(set.Breakdown) != 3 || set.Breakdown[2].Name != "Other" {
		t.Fatalf("breakdown = %+v, want top 2 + Other", set.Breakdown)
	}
	var sum float64
	for _, it := range set.Breakdown {
		sum += it.Cost
	}
	if sum != set.Total {
		t.Errorf("breakdown sums to %v, want %v", sum, set.Total)
	}
	if len(set.Daily) != 4 {
		t.Errorf("cloud daily points = %d, want 4", len(set.Daily))
	}
}

