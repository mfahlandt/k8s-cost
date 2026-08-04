// collect-scale builds the "Scale tests" tab dataset: per-prow-job cost on GCP
// (via the prow_k8s_io_job billing label) and per-account cost in the dedicated
// AWS scale boskos accounts (AWS has no activated prow job tag).
//
// Collection is incremental: raw (day, group, item, cost) rows live in
// data/scale/<cloud>.jsonl, a run only re-fetches the requested window and
// replaces those days, then re-aggregates the whole history into scale.json.
// So the yearly backfill happens once and daily runs stay cheap.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"

	"github.com/kubernetes/k8s-cost/internal/collector"
	"github.com/kubernetes/k8s-cost/internal/report"
)

// DefaultScaleAWSAccounts are the dedicated scale boskos accounts
// (k8s-infra-e2e-boskos-scale-001 / -002). Everything running there is a scale
// test, which is why they are a valid stand-in for the missing job tag.
var DefaultScaleAWSAccounts = map[string]string{
	"226543828060": "k8s-infra-e2e-boskos-scale-001",
	"405186867737": "k8s-infra-e2e-boskos-scale-002",
}

// scaleJobMatch restricts the GCP job list to scale/performance testing, so the
// tab does not turn into a full per-job cost explorer.
var scaleJobMatch = []string{"scale", "performance"}

const scaleTopN = 12

// scaleItemsPerDay caps the resources stored per (day, group); the tail goes
// into "Other". Keeps the committed row files small without changing totals.
const scaleItemsPerDay = 8

// errSkipCloud marks a cloud the caller asked to leave untouched; its stored
// rows are still rendered.
var errSkipCloud = errors.New("skipped")

// scaleCloud is one view of the scale spend, with its own row file and fetcher.
type scaleCloud struct {
	id        string
	groupKind string
	note      string
	fetch     func(ctx context.Context, start, end time.Time) ([]report.ScaleRow, error)
}

func cmdCollectScale(args []string) error {
	fs := flag.NewFlagSet("collect-scale", flag.ExitOnError)
	startFlag := fs.String("start", "", "start day YYYY-MM-DD (inclusive)")
	endFlag := fs.String("end", "", "end day YYYY-MM-DD (exclusive)")
	period := fs.String("period", "", "YYYY-MM shortcut for a whole month")
	project := fs.String("project", os.Getenv("GOOGLE_CLOUD_PROJECT"), "GCP project the BigQuery query runs in")
	table := fs.String("table", collector.DefaultTable, "GCP billing export table")
	location := fs.String("location", "US", "BigQuery dataset location")
	labelKey := fs.String("label-key", "prow_k8s_io_job", "GCP billing label carrying the prow job name")
	profile := fs.String("profile", os.Getenv("AWS_PROFILE"), "AWS shared-config profile")
	accountsFlag := fs.String("aws-accounts", "", "comma separated AWS account ids to treat as scale accounts (default: the two boskos scale accounts)")
	dataDir := fs.String("data", "./data", "data directory holding the raw scale rows")
	out := fs.String("out", "web/public/scale.json", "output JSON path")
	skipAWS := fs.Bool("skip-aws", false, "skip the AWS part (e.g. no credentials available)")
	skipGCP := fs.Bool("skip-gcp", false, "skip the GCP part")
	rebuildOnly := fs.Bool("rebuild-only", false, "query nothing, just re-render scale.json from the stored rows")
	_ = fs.Parse(args)

	var start, end time.Time
	if !*rebuildOnly {
		var err error
		start, end, err = resolveRange(*period, *startFlag, *endFlag)
		if err != nil {
			return err
		}
	}
	accounts := map[string]string{}
	if *accountsFlag == "" {
		accounts = DefaultScaleAWSAccounts
	} else {
		for _, id := range strings.Split(*accountsFlag, ",") {
			if id = strings.TrimSpace(id); id != "" {
				accounts[id] = id
			}
		}
	}

	requireGCPProject := func() error {
		if *skipGCP {
			return errSkipCloud
		}
		if *project == "" {
			return fmt.Errorf("--project (or GOOGLE_CLOUD_PROJECT) is required; pass --skip-gcp to omit GCP")
		}
		return nil
	}

	clouds := []scaleCloud{
		{
			id:        "gcp",
			groupKind: "job",
			note: "Cost per prow job from the BigQuery billing export label `" + *labelKey +
				"`, net of committed-use/sustained-use discounts (tax and adjustments excluded). " +
				"Only jobs whose name contains \"scale\" or \"performance\" are shown. " +
				"The label only exists from 2026-04-28 onwards — for older months use the project view.",
			fetch: func(ctx context.Context, start, end time.Time) ([]report.ScaleRow, error) {
				if err := requireGCPProject(); err != nil {
					return nil, err
				}
				return fetchGCPJobRows(ctx, *project, *table, *location, *labelKey, start, end)
			},
		},
		{
			id:        "gcp-projects",
			groupKind: "project",
			note: "The same spend seen through the dedicated scale projects (every project id " +
				"containing \"scale\"), which covers the whole year — unlike the job label, which " +
				"only exists from 2026-04-28. Net of committed-use/sustained-use discounts.",
			fetch: func(ctx context.Context, start, end time.Time) ([]report.ScaleRow, error) {
				if err := requireGCPProject(); err != nil {
					return nil, err
				}
				return fetchGCPProjectRows(ctx, *project, *table, *location, start, end)
			},
		},
		{
			id:        "aws",
			groupKind: "account",
			note: "AWS has no activated `prow.k8s.io/job` cost allocation tag, so this is the " +
				"spend of the dedicated scale boskos accounts (UnblendedCost, refunds/credits excluded). " +
				"These accounts are credit funded — the figure is consumption at list price, not cash out.",
			fetch: func(ctx context.Context, start, end time.Time) ([]report.ScaleRow, error) {
				if *skipAWS {
					return nil, errSkipCloud
				}
				return fetchAWSAccountRows(ctx, *profile, accounts, start, end)
			},
		},
	}

	ctx := context.Background()
	doc := report.Scale{GeneratedAt: time.Now().UTC()}

	for _, c := range clouds {
		path := report.ScaleRowsPath(*dataDir, c.id)
		rows, err := report.LoadScaleRows(path)
		if err != nil {
			return fmt.Errorf("%s: %w", c.id, err)
		}
		before := len(rows)

		if !*rebuildOnly {
			fresh, err := c.fetch(ctx, start, end)
			switch {
			case errors.Is(err, errSkipCloud):
				fmt.Printf("%s: skipped, keeping %d stored rows\n", c.id, before)
			case err != nil:
				return fmt.Errorf("%s: %w", c.id, err)
			default:
				fresh = report.CompactScaleRows(fresh, scaleItemsPerDay)
				rows = report.MergeScaleRows(rows, fresh,
					start.Format("2006-01-02"), end.Format("2006-01-02"))
				if err := report.SaveScaleRows(path, rows); err != nil {
					return fmt.Errorf("%s: %w", c.id, err)
				}
				fmt.Printf("%s: fetched %d rows for the window, %d stored (was %d)\n",
					c.id, len(fresh), len(rows), before)
			}
		}
		if len(rows) == 0 {
			continue
		}
		lo, hi := report.ScaleRowsRange(rows)
		if doc.Start == "" || lo < doc.Start {
			doc.Start = lo
		}
		if hi > doc.End {
			doc.End = hi
		}
		doc.Clouds = append(doc.Clouds,
			report.BuildScaleSet(c.id, c.groupKind, c.note, "USD", rows, scaleTopN))
	}
	if len(doc.Clouds) == 0 {
		return fmt.Errorf("no scale data collected or stored yet")
	}

	if err := os.MkdirAll(dirOf(*out), 0o755); err != nil {
		return err
	}
	if err := report.WriteScaleJSON(*out, doc); err != nil {
		return err
	}
	for _, c := range doc.Clouds {
		fmt.Printf("%s: %d %ss, total %.2f %s\n", c.Cloud, len(c.Groups), c.GroupKind, c.Total, c.Currency)
	}
	fmt.Printf("wrote %s (%s .. %s)\n", *out, doc.Start, doc.End)
	return nil
}

// fetchGCPJobRows pulls (day, job, sku) cost and keeps scale/performance jobs.
func fetchGCPJobRows(ctx context.Context, project, table, location, labelKey string, start, end time.Time) ([]report.ScaleRow, error) {
	rows, err := collector.QueryGCP(ctx, collector.GCPQueryConfig{
		BillingProject: project,
		Table:          table,
		Start:          start,
		End:            end,
		Location:       location,
		LabelKey:       labelKey,
		GroupBy:        []string{"label:" + labelKey, "sku"},
	})
	if err != nil {
		return nil, err
	}
	var out []report.ScaleRow
	for _, r := range rows {
		job := keyAt(r.Keys, 0)
		if !isScaleJob(job) {
			continue
		}
		out = append(out, report.ScaleRow{
			Date: r.Start, Group: job, Item: keyAt(r.Keys, 1), Cost: r.Amount, Gross: r.Gross,
		})
	}
	return out, nil
}

// fetchGCPProjectRows is the label-independent view: the dedicated scale
// projects (k8s-infra-e2e-scale-5k-project and the boskos-scale-NN pool).
// Grouped by service rather than SKU — with ~30 projects the per-SKU daily
// detail explodes the stored rows, and the SKU split is already available in
// the per-job view.
func fetchGCPProjectRows(ctx context.Context, project, table, location string, start, end time.Time) ([]report.ScaleRow, error) {
	rows, err := collector.QueryGCP(ctx, collector.GCPQueryConfig{
		BillingProject: project,
		Table:          table,
		Start:          start,
		End:            end,
		Location:       location,
		ProjectLike:    "%scale%",
		GroupBy:        []string{"project", "service"},
	})
	if err != nil {
		return nil, err
	}
	out := make([]report.ScaleRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.ScaleRow{
			Date: r.Start, Group: keyAt(r.Keys, 0), Item: keyAt(r.Keys, 1), Cost: r.Amount, Gross: r.Gross,
		})
	}
	return out, nil
}

// fetchAWSAccountRows pulls (day, account, usage type) cost for the scale accounts.
func fetchAWSAccountRows(ctx context.Context, profile string, accounts map[string]string, start, end time.Time) ([]report.ScaleRow, error) {
	ids := make([]string, 0, len(accounts))
	for id := range accounts {
		ids = append(ids, id)
	}
	rows, err := collector.QueryAWS(ctx, collector.AWSQueryConfig{
		Start:            start,
		End:              end,
		Profile:          profile,
		Granularity:      cetypes.GranularityDaily,
		GroupBy:          []string{"LINKED_ACCOUNT", "USAGE_TYPE"},
		DimensionFilters: map[string][]string{"LINKED_ACCOUNT": ids},
	})
	if err != nil {
		return nil, err
	}
	out := make([]report.ScaleRow, 0, len(rows))
	for _, r := range rows {
		id := keyAt(r.Keys, 0)
		name := accounts[id]
		if name == "" {
			name = id
		}
		out = append(out, report.ScaleRow{
			Date: r.Start, Group: name, Item: keyAt(r.Keys, 1), Cost: r.Amount, Gross: r.Gross,
		})
	}
	return out, nil
}

func isScaleJob(job string) bool {
	j := strings.ToLower(job)
	for _, m := range scaleJobMatch {
		if strings.Contains(j, m) {
			return true
		}
	}
	return false
}

func keyAt(keys []string, i int) string {
	if i < len(keys) {
		return keys[i]
	}
	return ""
}







