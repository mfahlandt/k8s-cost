# k8s-cost

**📊 Live dashboard: <https://mfahlandt.github.io/k8s-cost/>**
· [Overview](https://mfahlandt.github.io/k8s-cost/#/overview)
· [Top spenders](https://mfahlandt.github.io/k8s-cost/#/services)
· [Scale tests](https://mfahlandt.github.io/k8s-cost/#/scale)

Automates the monthly Kubernetes cloud-spend report. Upload provider billing
exports → normalize into a git-tracked, file-based data store → compute
per-provider metrics, burn rate and budget projections → publish a React
dashboard (GitHub Pages) + XLSX report, with Slack budget alerts.

No database. No Python or Node.js backend — all server-side tooling is Go. The
dataset lives in `data/` as diff-friendly JSON and can be published statically.

## Repo layout (monorepo)

```
cmd/costctl/          Go CLI: import, budget, report, alert
internal/model/       normalized domain types (DailySpend, BudgetConfig, ...)
internal/store/       file-based JSON store (data/spend, data/budgets)
internal/importer/    provider CSV parsers (aws-csv, gcp-csv, digitalocean-csv)
internal/calc/        metric + budget-projection engine (unit-tested)
internal/report/      XLSX workbook + dashboard.json generation
internal/notify/      Slack webhook alerts
web/                  React (Vite) dashboard, deployable to GitHub Pages
data/                 committed dataset (spend + budgets per provider)
examples/             sample billing exports
.github/workflows/    build+deploy (Pages) and scheduled budget alerts
```

## Quick start

```bash
# 1. Import billing exports
go run ./cmd/costctl import --provider aws --format aws-csv --file examples/aws-sample.csv
go run ./cmd/costctl import --provider gcp --format gcp-csv --file examples/gcp-may-2026.csv --period 2026-05

# 2. Configure annual budgets (with an alert threshold)
go run ./cmd/costctl budget --provider aws --year 2026 --amount 60000  --threshold 0.9
go run ./cmd/costctl budget --provider gcp --year 2026 --amount 800000 --threshold 0.9

# 3. Generate the dashboard JSON + XLSX report
go run ./cmd/costctl report --asof 2026-05-31

# 4. Run the dashboard locally
make dev   # http://localhost:5173
```

Or use the Makefile: `make build`, `make test`, `make report`, `make web`.

## Ad-hoc breakdowns (`query-aws` / `query-gcp`)

Beyond the tracked dataset, `costctl` can answer one-off questions such as *"what
do the upstream scale tests cost, per resource type?"*. These commands print a
table (or write CSV) and never write to `data/`:

```bash
# AWS: per service + usage type for the dedicated scale boskos accounts
go run ./cmd/costctl query-aws --profile k8s \
  --start 2026-07-01 --end 2026-08-01 --granularity MONTHLY \
  --filter "LINKED_ACCOUNT=226543828060,405186867737" \
  --group-by SERVICE --group-by USAGE_TYPE

# AWS: per prow job (requires the tag to be activated for cost allocation)
go run ./cmd/costctl query-aws --tag "prow.k8s.io/job=<job-name>" --group-by SERVICE \
  --start 2026-07-27 --end 2026-07-29

# GCP: per prow job (the `prow_k8s_io_job` label exists in the billing export)
go run ./cmd/costctl query-gcp --project kubernetes-public \
  --start 2026-07-01 --end 2026-08-01 --label-key prow_k8s_io_job --group-by job

# GCP: per project / SKU from the BigQuery billing export
go run ./cmd/costctl query-gcp --project kubernetes-public \
  --start 2026-07-01 --end 2026-08-01 \
  --projects k8s-infra-e2e-scale-5k-project --group-by service --group-by sku
```

See [`docs/SCALE_TESTS.md`](docs/SCALE_TESTS.md) for the scale-test numbers:
per-job attribution already works on GCP via labels, while AWS first needs
`prow.k8s.io/job` activated as a cost allocation tag.

## Drop-folder ingestion (no API? just commit the file)

Some providers have no automatable export (e.g. Azure). For those, drop the CSV
into `incoming/<provider>/` and commit it — the `ingest.yml` GitHub Action
parses it, updates the store, regenerates the dashboard and commits the result
back. The raw file is archived out-of-tree (see `.gitignore`).

```bash
# e.g. Azure: put the portal usage export here and push
cp AzureUsage.csv incoming/azure/AzureUsage.csv

# or run the same ingestion locally
go run ./cmd/costctl ingest --dir incoming --data ./data
go run ./cmd/costctl report --data ./data
```

The subfolder name selects the provider + format (`incoming/azure/` → `azure-csv`,
`incoming/aws/` → `aws-csv`, …). See `incoming/README.md` for the full mapping.

## Import formats

| Format             | Provider     | Notes |
|--------------------|--------------|-------|
| `aws-csv`          | AWS          | Cost Explorer / CUR style CSV with date + service + cost columns |
| `gcp-csv`          | GCP          | BigQuery billing export CSV. The provided service-level monthly SQL has no date column — pass `--period YYYY-MM` |
| `digitalocean-csv` | DigitalOcean | Billing history CSV (PDF/API collectors planned) |
| `azure-csv`        | Azure        | Portal "Download usage" / Cost Management CSV (one row per resource per day; summed to per-day/service totals) |

Column matching is alias-based and case-insensitive, so minor header
differences between exports are tolerated. Amounts accept `$1,234.56`,
`(123.45)` (negative) and plain numbers.

### GCP note

The monthly BigQuery query aggregates by service for a single month, so all its
rows are dated to the first of `--period`. Compute metrics with `--asof` set to
the **end of that month** for meaningful daily averages.

## Calculation logic

Implemented in `internal/calc` (see `calc_test.go` for a worked example):

```
lastMonthAvgDaily      = prevMonthTotal / daysInPrevMonth
monthlySpend           = Σ daily spend in current month
yearlyBasedOnThisMonth = monthlySpend * 12
ytd                    = Σ daily spend in current year up to asOf
currentMonthAvgDaily   = monthlySpend / daysElapsedInMonth
burnRate31Dec          = ytd + remainingDaysInYear * currentMonthAvgDaily
dailyAvgOverOneYear    = (monthlySpend / daysInCurrentMonth) * 12 / 365

projectedMonthlyAvg        = currentMonthAvgDaily * daysInCurrentMonth
projectedYearTotal         = ytd + remainingMonths * projectedMonthlyAvg
budgetUtilization          = projectedYearTotal / annualBudget
budgetAlert                = budgetUtilization >= alertThreshold
monthsUntilBudgetExhausted = annualBudget / projectedMonthlyAvg - monthsElapsed
```

## Deployment (GitHub Pages)

The dashboard is live at **<https://mfahlandt.github.io/k8s-cost/>**.

`.github/workflows/deploy.yml` builds Go, regenerates `dashboard.json` + the
XLSX from `data/`, builds the React app and deploys `web/dist` to Pages on every
push to `main`. Enable Pages (Settings → Pages → Source: GitHub Actions) and set
the Vite `base` in `web/vite.config.js` (or the `VITE_BASE` env var) to your
repo path.

## Slack alerts

`.github/workflows/alerts.yml` runs monthly and posts budget alerts when any
provider's projected year total crosses its threshold. Set the
`SLACK_WEBHOOK_URL` repository secret to enable it. Run manually with:

```bash
SLACK_WEBHOOK_URL=... go run ./cmd/costctl alert
```

## Roadmap

- Additional providers: Fastly, IBM Power CSV profiles
- DigitalOcean PDF invoice parser + billing API collector
- Optional cloud billing API collectors (AWS CE, GCP BigQuery) for automation
- Historical trend charts in the dashboard
```

