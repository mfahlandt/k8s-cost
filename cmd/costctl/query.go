// Ad-hoc cost breakdown queries. These do not touch the store: they answer
// one-off questions like "what did prow job X cost, broken down per AWS
// service / usage type?" and print a table or CSV.
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"

	"github.com/kubernetes/k8s-cost/internal/collector"
)

// stringList collects repeatable flags (--tag a=b --tag c=d).
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func cmdQueryAWS(args []string) error {
	fs := flag.NewFlagSet("query-aws", flag.ExitOnError)
	startFlag := fs.String("start", "", "start day YYYY-MM-DD (inclusive)")
	endFlag := fs.String("end", "", "end day YYYY-MM-DD (exclusive)")
	period := fs.String("period", "", "YYYY-MM shortcut for a whole month")
	granularity := fs.String("granularity", "DAILY", "DAILY, MONTHLY or HOURLY")
	metric := fs.String("metric", "UnblendedCost", "UnblendedCost, AmortizedCost, UsageQuantity, ...")
	profile := fs.String("profile", os.Getenv("AWS_PROFILE"), "AWS shared-config profile")
	region := fs.String("region", "", "AWS region (default us-east-1)")
	csvOut := fs.String("csv", "", "write the result as CSV to this path (default: table on stdout)")
	includeRC := fs.Bool("include-refunds-credits", false, "include Refund and Credit record types")
	listTagValues := fs.String("list-tag-values", "", "instead of costs, list the known values of this cost-allocation tag key")
	var groupBy, tags, dims stringList
	fs.Var(&groupBy, "group-by", "group key, repeatable (max 2): SERVICE, USAGE_TYPE, RECORD_TYPE, REGION, INSTANCE_TYPE, TAG:<key>")
	fs.Var(&tags, "tag", "tag filter key=value, repeatable (values may be comma separated)")
	fs.Var(&dims, "filter", "dimension filter KEY=value, repeatable (e.g. SERVICE=Amazon Elastic Compute Cloud - Compute)")
	_ = fs.Parse(args)

	start, end, err := resolveRange(*period, *startFlag, *endFlag)
	if err != nil {
		return err
	}
	if len(groupBy) == 0 {
		groupBy = stringList{"SERVICE"}
	}
	tagFilters, err := parseKeyValues(tags)
	if err != nil {
		return err
	}
	dimFilters, err := parseKeyValues(dims)
	if err != nil {
		return err
	}

	cfg := collector.AWSQueryConfig{
		Start:                 start,
		End:                   end,
		Profile:               *profile,
		Region:                *region,
		Metric:                *metric,
		Granularity:           cetypes.Granularity(strings.ToUpper(*granularity)),
		GroupBy:               groupBy,
		TagFilters:            tagFilters,
		DimensionFilters:      dimFilters,
		IncludeRefundsCredits: *includeRC,
	}

	if *listTagValues != "" {
		values, err := collector.ListAWSTagValues(context.Background(), cfg, *listTagValues, "")
		if err != nil {
			return err
		}
		if len(values) == 0 {
			fmt.Printf("no values for tag %q between %s and %s "+
				"(is it activated as a cost allocation tag?)\n",
				*listTagValues, start.Format("2006-01-02"), end.Format("2006-01-02"))
			return nil
		}
		for _, v := range values {
			fmt.Println(v)
		}
		return nil
	}

	rows, err := collector.QueryAWS(context.Background(), cfg)
	if err != nil {
		return err
	}
	return emitRows(rows, groupBy, *csvOut, fmt.Sprintf("AWS %s %s..%s",
		*metric, start.Format("2006-01-02"), end.Format("2006-01-02")))
}

func cmdQueryGCP(args []string) error {
	fs := flag.NewFlagSet("query-gcp", flag.ExitOnError)
	project := fs.String("project", os.Getenv("GOOGLE_CLOUD_PROJECT"), "GCP project the query runs in")
	table := fs.String("table", collector.DefaultTable, "fully-qualified billing export table")
	startFlag := fs.String("start", "", "start day YYYY-MM-DD (inclusive)")
	endFlag := fs.String("end", "", "end day YYYY-MM-DD (exclusive)")
	period := fs.String("period", "", "YYYY-MM shortcut for a whole month")
	location := fs.String("location", "US", "BigQuery dataset location")
	labelKey := fs.String("label-key", "", "restrict to resources carrying this label key")
	labelValue := fs.String("label-value", "", "restrict to this label value (requires --label-key)")
	labelSource := fs.String("label-source", "labels", "which labels to use: labels, system_labels, project_labels")
	listLabels := fs.Bool("list-labels", false, "instead of costs, list the label keys in the billing export (ranked by cost)")
	listLabelValues := fs.String("list-label-values", "", "instead of costs, list the values of this label key (ranked by cost)")
	projects := fs.String("projects", "", "comma separated billing project ids to restrict to")
	csvOut := fs.String("csv", "", "write the result as CSV to this path (default: table on stdout)")
	var groupBy stringList
	fs.Var(&groupBy, "group-by", "group key, repeatable (max 3): project, service, sku, region")
	_ = fs.Parse(args)

	if *project == "" {
		return fmt.Errorf("--project (or GOOGLE_CLOUD_PROJECT) is required")
	}
	start, end, err := resolveRange(*period, *startFlag, *endFlag)
	if err != nil {
		return err
	}
	if len(groupBy) == 0 {
		groupBy = stringList{"service"}
	}
	var projectList []string
	if *projects != "" {
		for _, p := range strings.Split(*projects, ",") {
			if p = strings.TrimSpace(p); p != "" {
				projectList = append(projectList, p)
			}
		}
	}

	if *listLabels || *listLabelValues != "" {
		stats, err := collector.ListGCPLabels(context.Background(), collector.GCPQueryConfig{
			BillingProject: *project,
			Table:          *table,
			Start:          start,
			End:            end,
			Location:       *location,
			LabelSource:    *labelSource,
			Projects:       projectList,
		}, *listLabelValues)
		if err != nil {
			return err
		}
		if len(stats) == 0 {
			fmt.Printf("no %s found between %s and %s\n", *labelSource,
				start.Format("2006-01-02"), end.Format("2006-01-02"))
			return nil
		}
		what := "label key"
		if *listLabelValues != "" {
			what = "value of " + *listLabelValues
		}
		fmt.Printf("%s (%s), %s..%s:\n", what, *labelSource,
			start.Format("2006-01-02"), end.Format("2006-01-02"))
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "name\tcost\trows")
		for _, s := range stats {
			fmt.Fprintf(tw, "%s\t%.2f\t%d\n", s.Name, s.Cost, s.Rows)
		}
		return tw.Flush()
	}

	rows, err := collector.QueryGCP(context.Background(), collector.GCPQueryConfig{
		BillingProject: *project,
		Table:          *table,
		Start:          start,
		End:            end,
		Location:       *location,
		GroupBy:        groupBy,
		LabelKey:       *labelKey,
		LabelValue:     *labelValue,
		LabelSource:    *labelSource,
		Projects:       projectList,
	})
	if err != nil {
		return err
	}
	return emitRows(rows, groupBy, *csvOut, fmt.Sprintf("GCP cost %s..%s",
		start.Format("2006-01-02"), end.Format("2006-01-02")))
}

// resolveRange turns --period or --start/--end into a [start, end) window.
func resolveRange(period, startFlag, endFlag string) (time.Time, time.Time, error) {
	switch {
	case period != "":
		t, err := time.Parse("2006-01", period)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --period %q: %w", period, err)
		}
		return t, t.AddDate(0, 1, 0), nil
	case startFlag != "" && endFlag != "":
		start, err := time.Parse("2006-01-02", startFlag)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --start: %w", err)
		}
		end, err := time.Parse("2006-01-02", endFlag)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --end: %w", err)
		}
		return start, end, nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("provide --period YYYY-MM or both --start and --end")
	}
}

// parseKeyValues parses repeated "key=v1,v2" flags into a map.
func parseKeyValues(items []string) (map[string][]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := map[string][]string{}
	for _, it := range items {
		k, v, ok := strings.Cut(it, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("invalid filter %q, expected key=value", it)
		}
		for _, val := range strings.Split(v, ",") {
			if val = strings.TrimSpace(val); val != "" {
				out[strings.TrimSpace(k)] = append(out[strings.TrimSpace(k)], val)
			}
		}
	}
	return out, nil
}

// emitRows prints the per-day rows plus a per-group total, either as an aligned
// table or as CSV.
func emitRows(rows []collector.CostRow, groupBy []string, csvPath, title string) error {
	if len(rows) == 0 {
		fmt.Println("no cost rows returned (check the time range, filters and that the tag/label is activated)")
		return nil
	}
	header := append([]string{"date"}, groupBy...)
	header = append(header, "amount", "unit")

	if csvPath != "" {
		f, err := os.Create(csvPath)
		if err != nil {
			return err
		}
		defer f.Close()
		w := csv.NewWriter(f)
		if err := w.Write(header); err != nil {
			return err
		}
		for _, r := range rows {
			rec := append([]string{r.Start}, padKeys(r.Keys, len(groupBy))...)
			rec = append(rec, strconv.FormatFloat(r.Amount, 'f', 6, 64), r.Unit)
			if err := w.Write(rec); err != nil {
				return err
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return err
		}
		fmt.Println("wrote", csvPath)
	} else {
		fmt.Println(title)
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, strings.Join(header, "\t"))
		for _, r := range rows {
			cells := append([]string{r.Start}, padKeys(r.Keys, len(groupBy))...)
			cells = append(cells, fmt.Sprintf("%.4f", r.Amount), r.Unit)
			fmt.Fprintln(tw, strings.Join(cells, "\t"))
		}
		tw.Flush()
	}

	// Totals per group combination, plus the grand total.
	totals := map[string]float64{}
	var grand, sumAbs float64
	unit := rows[0].Unit
	for _, r := range rows {
		totals[strings.Join(padKeys(r.Keys, len(groupBy)), " / ")] += r.Amount
		grand += r.Amount
		if r.Amount < 0 {
			sumAbs -= r.Amount
		} else {
			sumAbs += r.Amount
		}
	}
	// When credits cancel out the charges (the k8s accounts are credit funded,
	// so RECORD_TYPE=Credit mirrors the usage), the grand total collapses to ~0
	// and percentages of it are meaningless — fall back to the gross volume.
	denom := grand
	if denom < 0 {
		denom = -denom
	}
	if denom < 0.01*sumAbs {
		denom = sumAbs
	}
	type kv struct {
		k string
		v float64
	}
	list := make([]kv, 0, len(totals))
	for k, v := range totals {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })

	fmt.Printf("\nTotal per %s:\n", strings.Join(groupBy, " / "))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, e := range list {
		share := 0.0
		if denom != 0 {
			share = e.v / denom * 100
		}
		fmt.Fprintf(tw, "%s\t%.4f\t%.1f%%\n", e.k, e.v, share)
	}
	fmt.Fprintf(tw, "TOTAL\t%.4f %s\t\n", grand, unit)
	tw.Flush()
	return nil
}

func padKeys(keys []string, n int) []string {
	out := make([]string, n)
	for i := range out {
		if i < len(keys) {
			out[i] = keys[i]
		} else {
			out[i] = "(none)"
		}
	}
	return out
}
