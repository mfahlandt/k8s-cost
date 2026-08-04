package collector

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"
)

// GCPQueryConfig configures an ad-hoc breakdown query against the BigQuery
// billing export: cost per day, grouped by project / service / SKU, optionally
// restricted to resources carrying a given label (e.g. a prow job label) or to
// a set of projects (boskos projects used by a scale test).
type GCPQueryConfig struct {
	BillingProject string
	Table          string
	Start          time.Time
	End            time.Time
	Location       string

	// GroupBy selects the breakdown columns; supported values are
	// "project", "service", "sku", "region". Default: service.
	GroupBy []string

	// LabelKey/LabelValue filter on the resource/usage labels. LabelValue may
	// be empty to match any resource carrying the key.
	LabelKey   string
	LabelValue string

	// LabelSource selects which label array to use: "labels" (resource labels,
	// default), "system_labels" (GCP-set, e.g. compute.googleapis.com/machine_spec)
	// or "project_labels" (labels on the project).
	LabelSource string

	// Projects restricts to these billing project ids (ORed).
	Projects []string

	// ProjectLike restricts to project ids matching a SQL LIKE pattern, e.g.
	// "%scale%". Useful when the exact project list is unknown (the boskos
	// pools change over time).
	ProjectLike string
}

// labelColumn maps the CLI-facing label source to the billing export column.
func labelColumn(source string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "", "labels", "resource":
		return "labels", nil
	case "system", "system_labels":
		return "system_labels", nil
	case "project", "project_labels":
		return "project.labels", nil
	default:
		return "", fmt.Errorf("unsupported label source %q (labels, system_labels, project_labels)", source)
	}
}

// gcpQueryRow is a single breakdown row.
type gcpQueryRow struct {
	Day      bigquery.NullDate   `bigquery:"usage_day"`
	K1       bigquery.NullString `bigquery:"k1"`
	K2       bigquery.NullString `bigquery:"k2"`
	K3       bigquery.NullString `bigquery:"k3"`
	Subtotal float64             `bigquery:"subtotal"`
}

var gcpGroupExpr = map[string]string{
	"project": "project.id",
	"service": "service.description",
	"sku":     "sku.description",
	"region":  "location.region",
}

// validLabelKey guards the label key that gets inlined into the SQL (BigQuery
// does not allow query parameters inside a correlated UNNEST subquery here).
var validLabelKey = regexp.MustCompile(`^[A-Za-z0-9_.\-/]{1,128}$`)

// groupExpr resolves a --group-by token to a SQL expression. Besides the fixed
// dimensions it supports "label:<key>" (and the shorthand "job" for
// label:prow_k8s_io_job), which is how per-prow-job attribution works on GCP.
func groupExpr(token string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(token))
	if expr, ok := gcpGroupExpr[t]; ok {
		return expr, nil
	}
	key := ""
	switch {
	case t == "job":
		key = "prow_k8s_io_job"
	case strings.HasPrefix(t, "label:"):
		key = strings.TrimSpace(token[len("label:"):])
	default:
		return "", fmt.Errorf("unsupported --group-by %q (project, service, sku, region, job, label:<key>)", token)
	}
	if !validLabelKey.MatchString(key) {
		return "", fmt.Errorf("invalid label key %q", key)
	}
	return fmt.Sprintf("(SELECT l.value FROM UNNEST(labels) l WHERE l.key = '%s' LIMIT 1)", key), nil
}

// QueryGCP runs the ad-hoc breakdown and returns generic cost rows (the same
// shape as the AWS query output, so both can be printed/exported identically).
func QueryGCP(ctx context.Context, cfg GCPQueryConfig) ([]CostRow, error) {
	if cfg.BillingProject == "" {
		return nil, fmt.Errorf("BillingProject is required")
	}
	table := cfg.Table
	if table == "" {
		table = DefaultTable
	}
	groups := cfg.GroupBy
	if len(groups) == 0 {
		groups = []string{"service"}
	}
	if len(groups) > 3 {
		return nil, fmt.Errorf("at most 3 group-by keys supported, got %d", len(groups))
	}

	selects := make([]string, 3)
	for i := range selects {
		selects[i] = fmt.Sprintf("CAST(NULL AS STRING) AS k%d", i+1)
	}
	var groupCols []string
	for i, g := range groups {
		expr, err := groupExpr(g)
		if err != nil {
			return nil, err
		}
		selects[i] = fmt.Sprintf("%s AS k%d", expr, i+1)
		groupCols = append(groupCols, fmt.Sprintf("k%d", i+1))
	}

	where := []string{
		"cost_type != 'tax'",
		"cost_type != 'adjustment'",
		"usage_start_time >= @start",
		"usage_start_time < @end",
	}
	params := []bigquery.QueryParameter{
		{Name: "start", Value: cfg.Start},
		{Name: "end", Value: cfg.End},
	}
	if cfg.LabelKey != "" {
		col, err := labelColumn(cfg.LabelSource)
		if err != nil {
			return nil, err
		}
		if cfg.LabelValue != "" {
			where = append(where, fmt.Sprintf("EXISTS (SELECT 1 FROM UNNEST(%s) l WHERE l.key = @labelKey AND l.value = @labelValue)", col))
			params = append(params, bigquery.QueryParameter{Name: "labelValue", Value: cfg.LabelValue})
		} else {
			where = append(where, fmt.Sprintf("EXISTS (SELECT 1 FROM UNNEST(%s) l WHERE l.key = @labelKey)", col))
		}
		params = append(params, bigquery.QueryParameter{Name: "labelKey", Value: cfg.LabelKey})
	}
	if len(cfg.Projects) > 0 {
		where = append(where, "project.id IN UNNEST(@projects)")
		params = append(params, bigquery.QueryParameter{Name: "projects", Value: cfg.Projects})
	}
	if cfg.ProjectLike != "" {
		where = append(where, "project.id LIKE @projectLike")
		params = append(params, bigquery.QueryParameter{Name: "projectLike", Value: cfg.ProjectLike})
	}

	sql := fmt.Sprintf(`
WITH cost_data AS (
  SELECT
    DATE(usage_start_time, 'US/Pacific') AS usage_day,
    %s,
    cost,
    IFNULL((SELECT SUM(CAST(c.amount AS NUMERIC)) FROM UNNEST(credits) c
            WHERE c.type IN ('FEE_UTILIZATION_OFFSET','COMMITTED_USAGE_DISCOUNT_DOLLAR_BASE','COMMITTED_USAGE_DISCOUNT')), 0) AS cud_credits,
    IFNULL((SELECT SUM(CAST(c.amount AS NUMERIC)) FROM UNNEST(credits) c
            WHERE c.type IN ('SUSTAINED_USAGE_DISCOUNT','DISCOUNT','SUBSCRIPTION_BENEFIT')), 0) AS other_savings
  FROM `+"`%s`"+`
  WHERE %s
)
SELECT
  usage_day, k1, k2, k3,
  CAST(SUM(cost) + SUM(cud_credits) + SUM(other_savings) AS FLOAT64) AS subtotal
FROM cost_data
GROUP BY usage_day, k1, k2, k3
ORDER BY usage_day, %s
`, strings.Join(selects, ",\n    "), table, strings.Join(where, "\n    AND "), strings.Join(append(groupCols, "subtotal"), ", "))

	client, err := bigquery.NewClient(ctx, cfg.BillingProject)
	if err != nil {
		return nil, fmt.Errorf("bigquery client: %w", err)
	}
	defer client.Close()

	q := client.Query(sql)
	q.Parameters = params
	if cfg.Location != "" {
		q.Location = cfg.Location
	}
	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("run query: %w", err)
	}

	var out []CostRow
	for {
		var row gcpQueryRow
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
		day := row.Day.Date.String()
		next := row.Day.Date.AddDays(1).String()
		var keys []string
		for i, v := range []bigquery.NullString{row.K1, row.K2, row.K3} {
			if i >= len(groups) {
				break
			}
			keys = append(keys, v.StringVal)
		}
		out = append(out, CostRow{
			Start:  day,
			End:    next,
			Keys:   keys,
			Amount: row.Subtotal,
			Unit:   "USD",
		})
	}
	return out, nil
}

// LabelStat is one label key (or value) seen in the billing export, with the
// cost attributed to it. This is the GCP equivalent of Cost Explorer's GetTags:
// it answers "which labels can I actually break costs down by?".
type LabelStat struct {
	Name string  `json:"name"`
	Cost float64 `json:"cost"`
	Rows int64   `json:"rows"`
}

// ListGCPLabels returns the label keys present in the billing export for the
// window (when key is empty), or the values of that key (when key is set),
// ranked by attributed cost.
func ListGCPLabels(ctx context.Context, cfg GCPQueryConfig, key string) ([]LabelStat, error) {
	if cfg.BillingProject == "" {
		return nil, fmt.Errorf("BillingProject is required")
	}
	table := cfg.Table
	if table == "" {
		table = DefaultTable
	}
	col, err := labelColumn(cfg.LabelSource)
	if err != nil {
		return nil, err
	}

	where := []string{
		"usage_start_time >= @start",
		"usage_start_time < @end",
	}
	params := []bigquery.QueryParameter{
		{Name: "start", Value: cfg.Start},
		{Name: "end", Value: cfg.End},
	}
	if len(cfg.Projects) > 0 {
		where = append(where, "project.id IN UNNEST(@projects)")
		params = append(params, bigquery.QueryParameter{Name: "projects", Value: cfg.Projects})
	}
	selectExpr := "l.key"
	if key != "" {
		selectExpr = "l.value"
		where = append(where, "l.key = @labelKey")
		params = append(params, bigquery.QueryParameter{Name: "labelKey", Value: key})
	}

	sql := fmt.Sprintf(`
SELECT %s AS name, CAST(SUM(cost) AS FLOAT64) AS cost, COUNT(*) AS row_count
FROM `+"`%s`"+`, UNNEST(%s) l
WHERE %s
GROUP BY name
ORDER BY cost DESC
LIMIT 500
`, selectExpr, table, col, strings.Join(where, "\n  AND "))

	client, err := bigquery.NewClient(ctx, cfg.BillingProject)
	if err != nil {
		return nil, fmt.Errorf("bigquery client: %w", err)
	}
	defer client.Close()

	q := client.Query(sql)
	q.Parameters = params
	if cfg.Location != "" {
		q.Location = cfg.Location
	}
	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("run query: %w", err)
	}

	var out []LabelStat
	for {
		var row struct {
			Name bigquery.NullString `bigquery:"name"`
			Cost float64             `bigquery:"cost"`
			Rows int64               `bigquery:"row_count"`
		}
		err := it.Next(&row)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		out = append(out, LabelStat{Name: row.Name.StringVal, Cost: row.Cost, Rows: row.Rows})
	}
	return out, nil
}
