package collector
import (
"math"
"testing"
)
// TestDOByProductReconciles verifies the per-product breakdown sums exactly to
// grossSpend, folding any rounding remainder into "(other)".
func TestDOByProductReconciles(t *testing.T) {
sum := doInvoiceSummary{
ProductCharges: doSummaryItem{
Name:   "Product usage charges",
Amount: "100.00",
Items: []doLineItem{
{Name: "Droplets", Amount: "70.00"},
{Name: "Spaces", Amount: "25.00"},
// 5.00 intentionally unaccounted -> should surface as "(other)"
},
},
Overages: doSummaryItem{Name: "Overages", Amount: "3.50"},
}
bp := sum.byProduct()
var total float64
for _, v := range bp {
total += v
}
if math.Abs(total-sum.grossSpend()) > 0.005 {
t.Fatalf("Σ byProduct = %.2f, want grossSpend %.2f", total, sum.grossSpend())
}
if math.Abs(bp["Droplets"]-70) > 0.005 || math.Abs(bp["Spaces"]-25) > 0.005 {
t.Errorf("product amounts wrong: %+v", bp)
}
if math.Abs(bp["Overages"]-3.5) > 0.005 {
t.Errorf("overages = %.2f, want 3.50", bp["Overages"])
}
if math.Abs(bp["(other)"]-5) > 0.005 {
t.Errorf("(other) = %.2f, want 5.00 (100 - 70 - 25)", bp["(other)"])
}
}
// TestDOByProductEmpty returns nil when no line items are present, so the caller
// falls back to a single gross-spend record.
func TestDOByProductEmpty(t *testing.T) {
sum := doInvoiceSummary{ProductCharges: doSummaryItem{Amount: "42.00"}}
if bp := sum.byProduct(); bp != nil {
t.Errorf("expected nil breakdown, got %+v", bp)
}
}
