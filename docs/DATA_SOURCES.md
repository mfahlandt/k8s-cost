# Data sources & real-data setup

This documents how the 2025 spreadsheet was structured, how the tool maps onto
it, and exactly what you need to provide so the GCP BigQuery collector can run
against real data.

## How the 2025 workbook maps to this tool

The old `k8s infra cost 2025.xlsx` has three sheets:

| Sheet | Purpose | Maps to |
|-------|---------|---------|
| `2025` | Weekly tracking per provider (Sat–Fri weeks), plus credits & "Day Zero" | future weekly view + credit tracking |
| `Monthly Spent` | Monthly report per provider (Jan–Dec) | **`calc.Metrics` + the XLSX report** (1:1) |
| `Weekly Spent` | empty | — |

The `Monthly Spent` block per provider matches our metrics almost exactly:

| Spreadsheet row | Tool field |
|-----------------|------------|
| Avg. Daily Spent | `currentMonthAvgDaily` (= monthlySpend / daysInMonth) |
| Monthly Spent | `monthlySpend` |
| Yearly Spent based on this month | `yearlyBasedOnThisMonth` |
| YTD | `ytd` |
| (burn rate) Total spend on 31 Dec | `burnRate31Dec` |
| This month average over the year | `dailyAvgOverOneYear` (× 365) |

### Verified against real numbers (GCP, 2025)
Cross-checked the day-grouped BigQuery query against the old `Monthly Spent`
sheet — **exact to the cent** for every fully-settled month:

| Month | Tool (BigQuery) | Old sheet |
|-------|-----------------|-----------|
| Jan | 157,960.53 | 157,960.53 |
| Feb | 149,425.03 | 149,425.03 |
| Mar | 170,894.86 | 170,894.86 |
| May | 155,622.03 | 155,622.03 |
| Jun | 160,488.71 | 160,488.71 |

Avg daily May = 155,622.03 / 31 = **5,020.07** (sheet: 5,020.065). Later months
(Aug–Nov) differ by a few hundred $ — late-arriving costs/credits booked after
the sheet snapshot. Full-year 2025 GCP YTD = **$2,465,277** (82% of the $3M
budget).

### ⚠️ Two export tables — use the standard one
The billing dataset has two exports for account `018801-93540E-22A20E`
("Kubernetes Project Billing Only"):

| Table | History | Use |
|-------|---------|-----|
| `gcp_billing_export_v1_...` (standard) | **2019 → now** | ✅ **use this** (full history) |
| `gcp_billing_export_resource_v1_...` (resource-level) | 2025-04 → now | ❌ starts April 2025, returns 0 for earlier months |

The original query used the *resource* table (only from 2025-04). The collector
now defaults to the **standard** table (`collector.DefaultTable`), which matches
the old sheet exactly across the whole year.

### Differences to be aware of (sanity check)
1. **"Yearly based on this month"** – the sheet uses `avgDaily × 365.25`
   (≈ `dailyAvgOverOneYear × 365`), while the tool currently uses
   `monthlySpend × 12`. For a 31-day month these differ ~1.9%. Say the word and
   I'll switch the tool to the `× 365.25` convention to match the sheet exactly.
2. **Credits / "Day Zero"** – the `2025` sheet tracks provider credit pools
   (GCP budget ≈ **$3,000,000/yr**, plus AWS/DigitalOcean credit balances) and
   computes a "Day Zero (reach $3M)" and "Days with $$$ cover". The tool models
   an annual budget + alert threshold, but not the depleting credit balance yet.
   This is the "early warning when budgets run out" feature — a good next step.
3. **Provider order** – sheet order is GCP, AWS, Azure, DigitalOcean, Fastly,
   IBM. The tool's report order is AWS, Azure, DigitalOcean, Fastly, GCP, IBM
   (trivially adjustable).
4. **Week convention** – AWS/GCP weekly pulls use **Sat–Fri** ranges (per the
   sheet note). Only relevant if we add the weekly view.

## GCP — BigQuery collector (real data)

The collector (`costctl collect-gcp`) runs a day-grouped version of your billing
query directly against BigQuery, so the store gets true daily granularity. It
groups by **`service.description`** as well, giving a per-service breakdown for
the top-spenders view; summed across services each day's total is identical to
the day-only query.

> **Service breakdown backfill.** AWS and GCP collectors now store per-service
> rows and use *replace-by-range* semantics (`Store.ReplaceSpendRange`), so a
> re-collect swaps the old day-total rows for per-service rows without
> double-counting. The daily workflow only re-collects the current + previous
> month, so older history keeps its day-total rows (shown as `(unspecified)` in
> the breakdown) until you re-collect the full range once. Azure already carries
> full service detail from its CSV import.

### What I need from you
1. **A billing/query project** — a GCP project the query runs in and is billed
   to (BigQuery on-demand query cost). Its id goes into `--project` /
   `GOOGLE_CLOUD_PROJECT`.
2. **Access to the export table**
   `kubernetes-public.kubernetes_public_billing.gcp_billing_export_resource_v1_018801_93540E_22A20E`.
   The identity running the query needs, on that dataset/table:
   - `roles/bigquery.dataViewer` (read the billing export), and
   - `roles/bigquery.jobUser` on the **billing project** (run queries).
3. **Credentials (ADC)** — one of:
   - `gcloud auth application-default login` (your user, if it already has
     access to the k8s-infra billing dataset), **or**
   - a service-account JSON key with the roles above:
     `export GOOGLE_APPLICATION_CREDENTIALS=/path/key.json`

> Note: `kubernetes-public` is a public project, but the **billing export
> dataset is not world-readable**. You must already be a k8s-infra billing admin
> (or have one grant your service account `dataViewer` on that dataset).

### Run it
```bash
export GOOGLE_CLOUD_PROJECT=kubernetes-public   # you have jobUser here
gcloud auth application-default login           # or set GOOGLE_APPLICATION_CREDENTIALS

# --table defaults to the standard export (collector.DefaultTable)
go run ./cmd/costctl collect-gcp \
  --project kubernetes-public \
  --period 2025-05 \
  --location US

go run ./cmd/costctl report --asof 2025-05-31
```

> `mario@kubermatic.com` already has read access to the dataset **and**
> `bigquery.jobUser` on `kubernetes-public`, so ADC (no key) is the simplest
> path. One month scans ~23 GB (~$0.14).

### CSV fallback (no BigQuery access)
Run your existing SQL in the BigQuery console, export the result to CSV, then:
```bash
go run ./cmd/costctl import --provider gcp --format gcp-csv --file gcp-may.csv --period 2025-05
```

## AWS — real data

### API collector (recommended)
`costctl collect-aws` pulls daily **UnblendedCost** from the Cost Explorer API
(`GetCostAndUsage`) — the same metric as the sheet's "AWS Cost Management" table.
It groups by **SERVICE**, so the store gets a per-service, per-day breakdown for
the top-spenders view; summed across services the day total is unchanged.

It uses the standard AWS credential chain (SSO / shared profile / env / IAM
role). Required IAM permission: **`ce:GetCostAndUsage`** (Cost Explorer must be
enabled; for the whole org, run it in the **management/payer account**). CE API
calls cost ~$0.01 each.

**Auth option A — AWS SSO (IAM Identity Center), recommended:**
```bash
aws configure sso                 # enter SSO start URL + region, pick account+role
aws sso login --profile k8s
go run ./cmd/costctl collect-aws --period 2026-06 --profile k8s
```

**Auth option B — IAM access key:**
```bash
aws configure                     # or: export AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
go run ./cmd/costctl collect-aws --period 2026-06
```

Backfill a range in one call:
```bash
go run ./cmd/costctl collect-aws --start 2026-01-01 --end 2026-07-01 --profile k8s
go run ./cmd/costctl report --asof 2026-07-03
```

### CSV fallback
Export the Cost Explorer cost table to CSV (sheet note: use a **Sat–Fri** range)
and import:
```bash
go run ./cmd/costctl import --provider aws --format aws-csv --file aws.csv
```

## DigitalOcean — real data

### API collector (recommended)
`costctl collect-do` pulls monthly invoices from the DigitalOcean billing API
(`/v2/customers/my/invoices`), mapping each `invoice_period` (YYYY-MM) + amount
to one record per month (DO bills monthly, no daily data). `--preview` adds the
current in-progress month (`invoice_preview`).

Needs a **DigitalOcean personal access token** with read scope:
```bash
export DIGITALOCEAN_TOKEN=dop_v1_...
go run ./cmd/costctl collect-do --preview
go run ./cmd/costctl report --asof 2026-07-03
```
Create the token: DO Console → API → Tokens → Generate New Token (read scope).

### CSV / PDF fallback
CSV import (`digitalocean-csv`) works for exported billing history. PDF invoice
parsing is a possible future addition.

## IBM Power — real data

### API collector
`costctl collect-ibm` exchanges an IBM Cloud IAM **API key** for an access token,
then pulls monthly usage (`/v4/accounts/{account}/usage/{YYYY-MM}`) and sums
`billable_cost` across resources — one record per month (IBM bills monthly).

Needs an IBM Cloud IAM API key with billing/usage read access:
```bash
export IBMCLOUD_API_KEY=...
go run ./cmd/costctl collect-ibm --start 2025-01-01 --end 2026-08-01
go run ./cmd/costctl report --asof 2026-07-03
```
Create the key: IBM Cloud Console → **Manage → Access (IAM) → API keys →
Create**. The account GUID is auto-discovered; override with `--account` if the
key can access multiple accounts.

There are two IBM accounts, each with its own key and budget:

| Provider | Account | GUID |
|----------|---------|------|
| `ibm-power` | K8sOnPower | efa47ec6fd45473a9e1fd6b7b8363f5c |
| `ibm-z` | K8sOnZ-s390x | 6efe35b964de46b0924c2e95fe410903 |

```bash
IBMCLOUD_API_KEY=$IBM_Z_API_KEY go run ./cmd/costctl collect-ibm \
  --provider ibm-z --account 6efe35b964de46b0924c2e95fe410903 \
  --start 2025-01-01 --end 2026-08-01
```

## Azure — CSV drop (no API collector)

Azure has no automated collector in this tool — the usage export is downloaded
manually from the portal. Instead of a one-off `import`, use the **drop folder**
so committing the file ingests it (locally or via CI).

### Get the export
Azure portal → **Cost Management + Billing → Cost analysis / Usage + charges →
Download** → CSV. The importer expects the classic usage schema:

```
SubscriptionName, SubscriptionGuid, Date, ResourceGuid, ServiceName,
ServiceType, ServiceRegion, ServiceResource, Quantity, Cost
```

Dates are US-style `M/D/YYYY`; there is **one row per resource per day**. The
`azure-csv` importer sums those rows to **per-day, per-`ServiceName` totals**
(`Aggregate: true`) so they survive the store's `(date, service)` merge. There
is no currency column, so amounts default to USD — pass `--currency` to override.

### Ingest it
```bash
# drop it in and commit → the ingest.yml Action does the rest
cp AzureUsage.csv incoming/azure/AzureUsage.csv

# or run the same ingestion locally
go run ./cmd/costctl ingest --dir incoming --data ./data
go run ./cmd/costctl report --data ./data
```

`ingest` scans `incoming/<provider>/*.csv`, imports each with that provider's
default format, merges into the store (idempotent — re-dropping a period just
updates it) and moves the raw file to `data/archive/` (git-ignored). Any provider
with a CSV format works the same way (`incoming/aws/`, `incoming/gcp/`, …).

### Budget
Azure's annual budget is **$500,000/yr** (`data/budgets/azure.json`):
```bash
go run ./cmd/costctl budget --provider azure --year 2026 --amount 500000
```

## Fastly — bandwidth usage (not dollars)

Fastly invoices for the project are **$0** (covered by a committed bandwidth
plan), so tracking dollars is meaningless. Instead `costctl collect-fastly`
pulls **bandwidth usage** from the Fastly Historical Stats API
(`/stats/aggregate?by=day`, aggregated across all services) and stores it in
**GB** (records carry the unit `GB`, not a currency). The dashboard renders
non-currency units as plain numbers.

Needs a Fastly API token with stats read access:
```bash
export FASTLY_API_TOKEN=...
go run ./cmd/costctl collect-fastly --start 2025-01-01 --end 2026-07-03
```
Create the token: Fastly console → **Account → API tokens** → scope
`global:read` (or `stats`). The bandwidth "max"/allowance can be set as a
budget in GB to alert on usage vs. the committed cap.






