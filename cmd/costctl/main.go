// Command costctl is the operator CLI for the k8s-cost tool. It imports billing
// exports into the file-based store, configures budgets, computes metrics and
// renders the dashboard JSON + XLSX report, and posts Slack budget alerts.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/kubernetes/k8s-cost/internal/calc"
	"github.com/kubernetes/k8s-cost/internal/collector"
	"github.com/kubernetes/k8s-cost/internal/importer"
	"github.com/kubernetes/k8s-cost/internal/model"
	"github.com/kubernetes/k8s-cost/internal/notify"
	"github.com/kubernetes/k8s-cost/internal/report"
	"github.com/kubernetes/k8s-cost/internal/store"

	"context"
	"flag"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "import":
		err = cmdImport(os.Args[2:])
	case "budget":
		err = cmdBudget(os.Args[2:])
	case "collect-gcp":
		err = cmdCollectGCP(os.Args[2:])
	case "collect-aws":
		err = cmdCollectAWS(os.Args[2:])
	case "collect-do":
		err = cmdCollectDO(os.Args[2:])
	case "collect-ibm":
		err = cmdCollectIBM(os.Args[2:])
	case "collect-fastly":
		err = cmdCollectFastly(os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "alert":
		err = cmdAlert(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `costctl - Kubernetes cloud spend tracker

Usage:
  costctl import  --provider <p> --format <fmt> --file <csv> [--currency USD] [--period YYYY-MM] [--data ./data]
  costctl budget  --provider <p> --year <y> --amount <n> [--threshold 0.9] [--currency USD] [--data ./data]
  costctl collect-gcp --project <billing-project> --table <fq-billing-table> --period YYYY-MM [--location US] [--data ./data]
  costctl collect-aws [--start YYYY-MM-DD] [--end YYYY-MM-DD] [--period YYYY-MM] [--profile <p>] [--data ./data]
  costctl collect-do  [--token <do-token>] [--preview] [--data ./data]
  costctl collect-ibm [--start YYYY-MM-DD] [--end YYYY-MM-DD] [--period YYYY-MM] [--provider ibm-power|ibm-z] [--account <guid>] [--data ./data]
  costctl collect-fastly [--start YYYY-MM-DD] [--end YYYY-MM-DD] [--token <fastly-token>] [--data ./data]
  costctl report  [--asof YYYY-MM-DD] [--data ./data] [--json web/public/dashboard.json] [--xlsx reports/report.xlsx]
  costctl alert   --webhook <url> [--asof YYYY-MM-DD] [--data ./data]

Import formats: aws-csv, gcp-csv, digitalocean-csv

collect-gcp requires Google Application Default Credentials (ADC):
  gcloud auth application-default login      # user creds, or
  export GOOGLE_APPLICATION_CREDENTIALS=key.json   # service-account key

collect-aws uses the standard AWS credential chain (SSO/profile/env/role):
  aws configure sso && aws sso login --profile <p>   # then --profile <p>, or
  export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=...
Needs IAM permission: ce:GetCostAndUsage

collect-do needs a DigitalOcean API token (read scope):
  export DIGITALOCEAN_TOKEN=dop_v1_...   # or pass --token

collect-ibm needs an IBM Cloud IAM API key:
  export IBMCLOUD_API_KEY=...            # IAM api key with billing/usage access

collect-fastly needs a Fastly API token (stats read); tracks bandwidth in GB
(Fastly invoices are $0 under the committed plan):
  export FASTLY_API_TOKEN=...
`)
}

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	providerFlag := fs.String("provider", "", "provider id (aws, gcp, digitalocean, ...)")
	format := fs.String("format", "", "import format (aws-csv, gcp-csv, digitalocean-csv)")
	file := fs.String("file", "", "path to the export file")
	currency := fs.String("currency", "USD", "default currency when the export omits one")
	period := fs.String("period", "", "YYYY-MM for month-aggregated exports without a date column")
	dataDir := fs.String("data", "./data", "data directory")
	_ = fs.Parse(args)

	if *providerFlag == "" || *format == "" || *file == "" {
		return fmt.Errorf("--provider, --format and --file are required")
	}
	provider, err := model.ParseProvider(*providerFlag)
	if err != nil {
		return err
	}
	imp, err := importer.Get(*format)
	if err != nil {
		return err
	}
	if imp.Provider() != provider {
		return fmt.Errorf("format %q is for provider %q, not %q", *format, imp.Provider(), provider)
	}

	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()

	records, err := imp.Parse(f, importer.Options{DefaultCurrency: *currency, PeriodMonth: *period})
	if err != nil {
		return err
	}
	st, err := store.New(*dataDir)
	if err != nil {
		return err
	}
	added, updated, err := st.MergeSpend(provider, records)
	if err != nil {
		return err
	}
	fmt.Printf("imported %s: %d records (%d added, %d updated)\n", provider, len(records), added, updated)
	return nil
}

func cmdBudget(args []string) error {
	fs := flag.NewFlagSet("budget", flag.ExitOnError)
	providerFlag := fs.String("provider", "", "provider id")
	year := fs.Int("year", time.Now().Year(), "budget year")
	amount := fs.Float64("amount", 0, "annual budget amount")
	threshold := fs.Float64("threshold", 0.9, "alert threshold as a fraction (0.9 = 90%)")
	currency := fs.String("currency", "USD", "budget currency")
	dataDir := fs.String("data", "./data", "data directory")
	_ = fs.Parse(args)

	if *providerFlag == "" || *amount <= 0 {
		return fmt.Errorf("--provider and a positive --amount are required")
	}
	provider, err := model.ParseProvider(*providerFlag)
	if err != nil {
		return err
	}
	st, err := store.New(*dataDir)
	if err != nil {
		return err
	}
	cfg := model.BudgetConfig{
		Provider:       provider,
		Year:           *year,
		AnnualBudget:   *amount,
		Currency:       *currency,
		AlertThreshold: *threshold,
	}
	if err := st.SaveBudget(cfg); err != nil {
		return err
	}
	fmt.Printf("saved budget for %s: %.2f %s (year %d, alert at %.0f%%)\n",
		provider, cfg.AnnualBudget, cfg.Currency, cfg.Year, cfg.AlertThreshold*100)
	return nil
}

func cmdCollectGCP(args []string) error {
	fs := flag.NewFlagSet("collect-gcp", flag.ExitOnError)
	project := fs.String("project", os.Getenv("GOOGLE_CLOUD_PROJECT"), "GCP billing project to run the query in")
	table := fs.String("table", collector.DefaultTable, "fully-qualified billing export table (project.dataset.table)")
	period := fs.String("period", "", "YYYY-MM month to collect")
	location := fs.String("location", "US", "BigQuery dataset location")
	dataDir := fs.String("data", "./data", "data directory")
	_ = fs.Parse(args)

	if *project == "" || *table == "" || *period == "" {
		return fmt.Errorf("--project (or GOOGLE_CLOUD_PROJECT), --table and --period are required")
	}
	start, err := time.Parse("2006-01", *period)
	if err != nil {
		return fmt.Errorf("invalid --period %q: %w", *period, err)
	}
	end := start.AddDate(0, 1, 0)

	records, err := collector.CollectGCP(context.Background(), collector.GCPConfig{
		BillingProject: *project,
		Table:          *table,
		Start:          start,
		End:            end,
		Location:       *location,
	})
	if err != nil {
		return err
	}
	st, err := store.New(*dataDir)
	if err != nil {
		return err
	}
	added, updated, err := st.MergeSpend(model.ProviderGCP, records)
	if err != nil {
		return err
	}
	fmt.Printf("collected gcp %s: %d daily records (%d added, %d updated)\n", *period, len(records), added, updated)
	return nil
}

func cmdCollectAWS(args []string) error {
	fs := flag.NewFlagSet("collect-aws", flag.ExitOnError)
	startFlag := fs.String("start", "", "start day YYYY-MM-DD (inclusive)")
	endFlag := fs.String("end", "", "end day YYYY-MM-DD (exclusive)")
	period := fs.String("period", "", "YYYY-MM shortcut for a whole month (overrides start/end)")
	profile := fs.String("profile", os.Getenv("AWS_PROFILE"), "AWS shared-config profile")
	region := fs.String("region", "", "AWS region (default us-east-1)")
	dataDir := fs.String("data", "./data", "data directory")
	_ = fs.Parse(args)

	var start, end time.Time
	switch {
	case *period != "":
		t, err := time.Parse("2006-01", *period)
		if err != nil {
			return fmt.Errorf("invalid --period %q: %w", *period, err)
		}
		start, end = t, t.AddDate(0, 1, 0)
	case *startFlag != "" && *endFlag != "":
		var err error
		if start, err = time.Parse("2006-01-02", *startFlag); err != nil {
			return fmt.Errorf("invalid --start: %w", err)
		}
		if end, err = time.Parse("2006-01-02", *endFlag); err != nil {
			return fmt.Errorf("invalid --end: %w", err)
		}
	default:
		return fmt.Errorf("provide --period YYYY-MM or both --start and --end")
	}

	records, err := collector.CollectAWS(context.Background(), collector.AWSConfig{
		Start:   start,
		End:     end,
		Profile: *profile,
		Region:  *region,
	})
	if err != nil {
		return err
	}
	st, err := store.New(*dataDir)
	if err != nil {
		return err
	}
	added, updated, err := st.MergeSpend(model.ProviderAWS, records)
	if err != nil {
		return err
	}
	fmt.Printf("collected aws %s..%s: %d daily records (%d added, %d updated)\n",
		start.Format("2006-01-02"), end.Format("2006-01-02"), len(records), added, updated)
	return nil
}

func cmdCollectDO(args []string) error {
	fs := flag.NewFlagSet("collect-do", flag.ExitOnError)
	token := fs.String("token", os.Getenv("DIGITALOCEAN_TOKEN"), "DigitalOcean API token (read scope)")
	preview := fs.Bool("preview", false, "include the current in-progress month (invoice_preview)")
	dataDir := fs.String("data", "./data", "data directory")
	_ = fs.Parse(args)

	if *token == "" {
		return fmt.Errorf("--token or DIGITALOCEAN_TOKEN is required")
	}
	records, err := collector.CollectDO(context.Background(), collector.DOConfig{
		Token:          *token,
		IncludePreview: *preview,
	})
	if err != nil {
		return err
	}
	st, err := store.New(*dataDir)
	if err != nil {
		return err
	}
	added, updated, err := st.MergeSpend(model.ProviderDigitalOcean, records)
	if err != nil {
		return err
	}
	fmt.Printf("collected digitalocean: %d monthly invoices (%d added, %d updated)\n", len(records), added, updated)
	return nil
}

func cmdCollectIBM(args []string) error {
	fs := flag.NewFlagSet("collect-ibm", flag.ExitOnError)
	apiKey := fs.String("apikey", os.Getenv("IBMCLOUD_API_KEY"), "IBM Cloud IAM API key")
	account := fs.String("account", os.Getenv("IBMCLOUD_ACCOUNT_ID"), "IBM Cloud account GUID (auto-discovered if empty)")
	providerFlag := fs.String("provider", "ibm-power", "provider to store under (ibm-power or ibm-z)")
	startFlag := fs.String("start", "", "start day YYYY-MM-DD (inclusive)")
	endFlag := fs.String("end", "", "end day YYYY-MM-DD (exclusive)")
	period := fs.String("period", "", "YYYY-MM shortcut for a single month")
	dataDir := fs.String("data", "./data", "data directory")
	_ = fs.Parse(args)

	if *apiKey == "" {
		return fmt.Errorf("--apikey or IBMCLOUD_API_KEY is required")
	}
	provider, err := model.ParseProvider(*providerFlag)
	if err != nil {
		return err
	}
	if provider != model.ProviderIBMPower && provider != model.ProviderIBMZ {
		return fmt.Errorf("--provider must be ibm-power or ibm-z, got %q", *providerFlag)
	}
	var start, end time.Time
	switch {
	case *period != "":
		t, err := time.Parse("2006-01", *period)
		if err != nil {
			return fmt.Errorf("invalid --period %q: %w", *period, err)
		}
		start, end = t, t.AddDate(0, 1, 0)
	case *startFlag != "" && *endFlag != "":
		var err error
		if start, err = time.Parse("2006-01-02", *startFlag); err != nil {
			return fmt.Errorf("invalid --start: %w", err)
		}
		if end, err = time.Parse("2006-01-02", *endFlag); err != nil {
			return fmt.Errorf("invalid --end: %w", err)
		}
	default:
		return fmt.Errorf("provide --period YYYY-MM or both --start and --end")
	}

	records, err := collector.CollectIBM(context.Background(), collector.IBMConfig{
		APIKey:    *apiKey,
		AccountID: *account,
		Provider:  provider,
		Start:     start,
		End:       end,
	})
	if err != nil {
		return err
	}
	st, err := store.New(*dataDir)
	if err != nil {
		return err
	}
	added, updated, err := st.MergeSpend(provider, records)
	if err != nil {
		return err
	}
	fmt.Printf("collected %s: %d monthly records (%d added, %d updated)\n", provider, len(records), added, updated)
	return nil
}

func cmdCollectFastly(args []string) error {
	fs := flag.NewFlagSet("collect-fastly", flag.ExitOnError)
	token := fs.String("token", os.Getenv("FASTLY_API_TOKEN"), "Fastly API token (stats read)")
	startFlag := fs.String("start", "", "start day YYYY-MM-DD (inclusive)")
	endFlag := fs.String("end", "", "end day YYYY-MM-DD (exclusive)")
	period := fs.String("period", "", "YYYY-MM shortcut for a single month")
	dataDir := fs.String("data", "./data", "data directory")
	_ = fs.Parse(args)

	if *token == "" {
		return fmt.Errorf("--token or FASTLY_API_TOKEN is required")
	}
	var start, end time.Time
	switch {
	case *period != "":
		t, err := time.Parse("2006-01", *period)
		if err != nil {
			return fmt.Errorf("invalid --period %q: %w", *period, err)
		}
		start, end = t, t.AddDate(0, 1, 0)
	case *startFlag != "" && *endFlag != "":
		var err error
		if start, err = time.Parse("2006-01-02", *startFlag); err != nil {
			return fmt.Errorf("invalid --start: %w", err)
		}
		if end, err = time.Parse("2006-01-02", *endFlag); err != nil {
			return fmt.Errorf("invalid --end: %w", err)
		}
	default:
		return fmt.Errorf("provide --period YYYY-MM or both --start and --end")
	}

	records, err := collector.CollectFastly(context.Background(), collector.FastlyConfig{
		Token: *token,
		Start: start,
		End:   end,
	})
	if err != nil {
		return err
	}
	st, err := store.New(*dataDir)
	if err != nil {
		return err
	}
	added, updated, err := st.MergeSpend(model.ProviderFastly, records)
	if err != nil {
		return err
	}
	fmt.Printf("collected fastly: %d daily bandwidth records in GB (%d added, %d updated)\n", len(records), added, updated)
	return nil
}

// computeAll loads every provider's data and computes metrics as of asOf.
func computeAll(st *store.Store, asOf time.Time) ([]calc.Metrics, error) {
	var metrics []calc.Metrics
	for _, p := range model.AllProviders {
		records, err := st.LoadSpend(p)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			continue // skip providers with no data yet
		}
		budget, err := st.LoadBudget(p, asOf.Year())
		if err != nil {
			return nil, err
		}
		metrics = append(metrics, calc.Compute(p, records, asOf, budget))
	}
	return metrics, nil
}

func parseAsOf(s string) (time.Time, error) {
	if s == "" {
		return time.Now().UTC(), nil
	}
	return time.Parse("2006-01-02", s)
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	asof := fs.String("asof", "", "reference date YYYY-MM-DD (default: today)")
	dataDir := fs.String("data", "./data", "data directory")
	jsonOut := fs.String("json", "web/public/dashboard.json", "dashboard JSON output path")
	xlsxOut := fs.String("xlsx", "reports/report.xlsx", "XLSX report output path")
	_ = fs.Parse(args)

	asOf, err := parseAsOf(*asof)
	if err != nil {
		return err
	}
	st, err := store.New(*dataDir)
	if err != nil {
		return err
	}
	metrics, err := computeAll(st, asOf)
	if err != nil {
		return err
	}
	dash := report.BuildDashboard(asOf, metrics)

	// One snapshot per elapsed month of the year: past months as-of their last
	// day, the current month as-of the report date — so the UI can switch.
	for mo := 1; mo <= int(asOf.Month()); mo++ {
		snapAsOf := time.Date(asOf.Year(), time.Month(mo)+1, 0, 0, 0, 0, 0, time.UTC) // last day of month
		if mo == int(asOf.Month()) {
			snapAsOf = asOf
		}
		snapMetrics, err := computeAll(st, snapAsOf)
		if err != nil {
			return err
		}
		label := fmt.Sprintf("%d-%02d", asOf.Year(), mo)
		dash.Snapshots = append(dash.Snapshots, report.BuildSnapshot(label, snapAsOf, snapMetrics))
	}

	if *jsonOut != "" {
		if err := os.MkdirAll(dirOf(*jsonOut), 0o755); err != nil {
			return err
		}
		if err := report.WriteJSON(*jsonOut, dash); err != nil {
			return err
		}
		fmt.Println("wrote", *jsonOut)
	}
	if *xlsxOut != "" {
		if err := os.MkdirAll(dirOf(*xlsxOut), 0o755); err != nil {
			return err
		}
		if err := report.WriteXLSX(*xlsxOut, dash); err != nil {
			return err
		}
		fmt.Println("wrote", *xlsxOut)
	}
	return nil
}

func cmdAlert(args []string) error {
	fs := flag.NewFlagSet("alert", flag.ExitOnError)
	webhook := fs.String("webhook", os.Getenv("SLACK_WEBHOOK_URL"), "Slack incoming webhook URL")
	asof := fs.String("asof", "", "reference date YYYY-MM-DD (default: today)")
	dataDir := fs.String("data", "./data", "data directory")
	_ = fs.Parse(args)

	if *webhook == "" {
		return fmt.Errorf("--webhook or SLACK_WEBHOOK_URL is required")
	}
	asOf, err := parseAsOf(*asof)
	if err != nil {
		return err
	}
	st, err := store.New(*dataDir)
	if err != nil {
		return err
	}
	metrics, err := computeAll(st, asOf)
	if err != nil {
		return err
	}
	if err := notify.NewSlack(*webhook).PostBudgetAlerts(metrics); err != nil {
		return err
	}
	fmt.Println("alert check complete")
	return nil
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}




























