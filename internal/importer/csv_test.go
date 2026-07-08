package importer
import (
"strings"
"testing"
"github.com/kubernetes/k8s-cost/internal/model"
)
// TestAzureAggregation verifies that the resource-granular Azure export (one
// row per resource per day) is summed into per-day/per-service totals so the
// store's (date, service)-keyed merge does not drop rows.
func TestAzureAggregation(t *testing.T) {
imp, err := Get("azure-csv")
if err != nil {
t.Fatal(err)
}
if imp.Provider() != model.ProviderAzure {
t.Fatalf("provider = %q, want azure", imp.Provider())
}
csv := strings.Join([]string{
"SubscriptionName,SubscriptionGuid,Date,ResourceGuid,ServiceName,ServiceType,ServiceRegion,ServiceResource,Quantity,Cost",
`"Sub","g","8/17/2024","r1","Storage","SSD","CH North","Disk","0.01","0.10"`,
`"Sub","g","8/17/2024","r2","Storage","SSD","CH North","Disk","0.02","0.20"`,
`"Sub","g","8/17/2024","r3","Virtual Machines","D2","CH North","VM","1","1.00"`,
`"Sub","g","8/18/2024","r1","Storage","SSD","CH North","Disk","0.03","0.30"`,
}, "\n")
recs, err := imp.Parse(strings.NewReader(csv), Options{DefaultCurrency: "USD"})
if err != nil {
t.Fatal(err)
}
// 3 distinct (date, service) keys: Storage 8/17, VMs 8/17, Storage 8/18.
if len(recs) != 3 {
t.Fatalf("got %d records, want 3 (aggregated): %+v", len(recs), recs)
}
got := map[string]float64{}
for _, r := range recs {
got[r.Date.String()+"/"+r.Service] = r.Amount
}
want := map[string]float64{
"2024-08-17/Storage":          0.30,
"2024-08-17/Virtual Machines": 1.00,
"2024-08-18/Storage":          0.30,
}
for k, w := range want {
if diff := got[k] - w; diff > 1e-9 || diff < -1e-9 {
t.Errorf("%s = %v, want %v", k, got[k], w)
}
}
}
