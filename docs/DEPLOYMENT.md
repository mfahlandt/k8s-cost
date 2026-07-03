# Deployment: GitHub Pages + daily cron

The dashboard is fully static (React build + `dashboard.json`), the data store
is file-based in `data/` — no server, no database. A GitHub Actions workflow
(`.github/workflows/update.yml`) refreshes everything **daily at 06:00 UTC**:

```
cron 06:00 UTC
  → collect AWS / GCP / DO / IBM Power / IBM Z / Fastly (APIs)
  → costctl report  (dashboard.json + XLSX, month snapshots)
  → commit data/ + dashboard.json back to the repo   ← history lives in git
  → build web/ → deploy to GitHub Pages
  → Slack alert if any budget crosses its threshold
```

## One-time setup

### 1. Enable GitHub Pages
Repo → **Settings → Pages** → Source: **GitHub Actions**.
The site will be at `https://<org>.github.io/k8s-cost/`.
(If the repo has a different name, set `VITE_BASE=/<repo>/` in the build step.)

### 2. Add repository secrets
Repo → **Settings → Secrets and variables → Actions → New repository secret**.
Copy the values from your local `.env` (never commit that file):

| Secret | From `.env` | Used by |
|--------|-------------|---------|
| `AWS_ACCESS_KEY_ID` | `AWS_ACCESS_KEY_ID` | collect-aws |
| `AWS_SECRET_ACCESS_KEY` | `AWS_SECRET_ACCESS_KEY` | collect-aws |
| `DIGITALOCEAN_TOKEN` | `DIGITALOCEAN_TOKEN` | collect-do |
| `IBMCLOUD_API_KEY` | `IBMCLOUD_API_KEY` | collect-ibm (Power) |
| `IBM_Z_API_KEY` | `IBM_Z_API_KEY` | collect-ibm (Z) |
| `FASTLY_API_TOKEN` | `FASTLY_API_TOKEN` | collect-fastly |
| `GCP_SA_KEY` | service-account JSON (see below) | collect-gcp |
| `SLACK_WEBHOOK_URL` | `SLACK_WEBHOOK_URL` | budget alerts |

Every collector step is optional: if its secret is missing the step is skipped
and the rest still runs.

> 🔴 **Rotate the AWS access key before adding it** — the current one was
> exposed in chat. IAM → user Mario → Security credentials → create new key,
> deactivate old.

### 3. GCP credentials (two options)

**Option A — your user's ADC file (quickest, no admin needed).** Your Google
user already has dataset access + jobUser, and you can't create service
accounts in `kubernetes-public` (no IAM admin). The ADC file works as a
credentials JSON in CI:

```bash
gcloud auth application-default login
gcloud auth application-default set-quota-project kubernetes-public
cat ~/.config/gcloud/application_default_credentials.json   # → GCP_SA_KEY secret
```

Paste that JSON as the `GCP_SA_KEY` secret. Caveats: it's a personal refresh
token — it dies if you revoke sessions / change password, and CI then acts as
you. Fine to start, replace with Option B later.

**Option B — dedicated service account (cleaner, needs a k8s-infra admin).**
Send `scripts/gcp-create-sa.sh` to a k8s-infra admin — it creates the SA in
`kubernetes-public` with exactly the two required grants (jobUser on the
project, dataViewer on the billing dataset) and emits the key JSON. Paste that
as `GCP_SA_KEY`. (Best long-term: Workload Identity Federation.)

The workflow accepts **either** JSON type — it writes the secret to a file and
sets `GOOGLE_APPLICATION_CREDENTIALS`.

### 4. Push and run
```bash
git push
```
Then trigger the first run manually: **Actions → Update cost dashboard → Run
workflow** — or wait for the 06:00 UTC cron.

## What the daily window covers
Collectors fetch **previous month start → tomorrow**, so late-landing costs
(GCP posts up to ~24h late, AWS finalizes over days) self-correct on each run.
`MergeSpend` upserts by date, so re-collecting the same days is idempotent.

## Budgets
Budgets are per **provider + year** in `data/budgets/*.json` (they reset Jan 1):

| Provider | 2026 budget |
|----------|-------------|
| AWS | $3,000,000 |
| GCP | $3,000,000 |
| DigitalOcean | $17,000 |
| IBM Power | $45,000 |
| IBM Z | $55,000 |
| Fastly | 120,000,000 GB (= 10 PB/month) |

Change with: `go run ./cmd/costctl budget --provider <p> --year <y> --amount <n> [--currency GB]`.



