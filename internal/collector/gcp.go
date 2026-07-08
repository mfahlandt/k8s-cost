// Package collector implements optional cloud billing API collectors that pull
// spend directly from provider APIs into normalized model.DailySpend records.
//
// The GCP collector queries the BigQuery billing export. It is a faithful,
// day-grouped adaptation of the k8s-infra monthly service query: it applies the
// same cost_type filters and CUD/credit adjustments, but groups by usage day so
// the file-based store receives true daily granularity (which improves burn-rate
// accuracy vs. month-aggregated CSV imports).
package collector

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"

	"github.com/kubernetes/k8s-cost/internal/model"
)

// DefaultTable is the recommended k8s-infra billing export table.
//
// Use the *standard* export (gcp_billing_export_v1_...), which holds the full
// history (data back to 2019). The resource-level export
// (gcp_billing_export_resource_v1_...) only starts 2025-04 and will silently
// return zero for earlier months. The day-grouped query here only needs
// cost/credits/cost_type, all present in the standard export.
const DefaultTable = "kubernetes-public.kubernetes_public_billing.gcp_billing_export_v1_018801_93540E_22A20E"

// GCPConfig configures a BigQuery billing collection run.
type GCPConfig struct {
	// BillingProject is the GCP project used to run (and bill) the query. This
	// is your own project, not necessarily the one owning the export table.
	BillingProject string
	// Table is the fully-qualified billing export table, e.g.
	// kubernetes-public.kubernetes_public_billing.gcp_billing_export_resource_v1_018801_93540E_22A20E
	Table string
	// Start and End bound the query on usage_start_time (End exclusive).
	Start time.Time
	End   time.Time
	// Location is the BigQuery dataset location (e.g. "US"). Optional.
	Location string
}

// dailyRow is a single day's aggregated cost returned by the query.
type dailyRow struct {
	Day      bigquery.NullDate   `bigquery:"usage_day"`
	Service  bigquery.NullString `bigquery:"service"`
	Subtotal float64             `bigquery:"subtotal"`
}

// query mirrors the k8s-infra billing query's adjustments but groups by day and
// service. The Subtotal expression matches the original:
//
//	cost + cud_credits + other_savings
//
// excluding tax/adjustment cost types, over the given usage window. Grouping by
// service.description adds the per-service breakdown for the top-spenders view;
// summed across services a day's total is identical to the day-only query.
const gcpDailyQueryTemplate = `
WITH cost_data AS (
  SELECT
    DATE(usage_start_time, 'US/Pacific') AS usage_day,
    service.description AS service,
    cost,
    IFNULL((SELECT SUM(CAST(c.amount AS NUMERIC)) FROM UNNEST(credits) c
            WHERE c.type IN ('COMMITTED_USAGE_DISCOUNT','COMMITTED_USAGE_DISCOUNT_DOLLAR_BASE')), 0) AS cud_credits,
    IFNULL((SELECT SUM(CAST(c.amount AS NUMERIC)) FROM UNNEST(credits) c
            WHERE c.type IN ('SUSTAINED_USAGE_DISCOUNT','DISCOUNT','SUBSCRIPTION_BENEFIT')), 0) AS other_savings
  FROM ` + "`%s`" + `
  WHERE cost_type != 'tax'
    AND cost_type != 'adjustment'
    AND usage_start_time >= @start
    AND usage_start_time < @end
)
SELECT
  usage_day,
  service,
  CAST(SUM(cost) + SUM(cud_credits) + SUM(other_savings) AS FLOAT64) AS subtotal
FROM cost_data
GROUP BY usage_day, service
ORDER BY usage_day, service
`

// CollectGCP runs the BigQuery billing query and returns daily spend records.
func CollectGCP(ctx context.Context, cfg GCPConfig) ([]model.DailySpend, error) {
	if cfg.BillingProject == "" {
		return nil, fmt.Errorf("BillingProject is required")
	}
	if cfg.Table == "" {
		return nil, fmt.Errorf("Table is required")
	}
	client, err := bigquery.NewClient(ctx, cfg.BillingProject)
	if err != nil {
		return nil, fmt.Errorf("bigquery client: %w", err)
	}
	defer client.Close()

	q := client.Query(fmt.Sprintf(gcpDailyQueryTemplate, cfg.Table))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "start", Value: cfg.Start},
		{Name: "end", Value: cfg.End},
	}
	if cfg.Location != "" {
		q.Location = cfg.Location
	}

	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("run query: %w", err)
	}

	var out []model.DailySpend
	for {
		var row dailyRow
		err := it.Next(&row)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		if !row.Day.Valid {
			continue
		}
		d := row.Day.Date // civil.Date
		when := time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
		out = append(out, model.DailySpend{
			Provider: model.ProviderGCP,
			Date:     model.NewDate(when),
			Amount:   row.Subtotal,
			Currency: "USD",
			Service:  row.Service.StringVal,
		})
	}
	return out, nil
}



