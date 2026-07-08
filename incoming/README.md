# Drop folder for billing exports
Drop a provider billing export here and commit it — the
`.github/workflows/ingest.yml` action ingests it automatically on push.
## Layout
```
incoming/
  <provider>/
    <anything>.csv
```
The subfolder name is the provider id and selects the import format:
| Folder                   | Provider     | Format             |
|--------------------------|--------------|--------------------|
| `incoming/azure/`        | Azure        | `azure-csv`        |
| `incoming/aws/`          | AWS          | `aws-csv`          |
| `incoming/gcp/`          | GCP          | `gcp-csv`          |
| `incoming/digitalocean/` | DigitalOcean | `digitalocean-csv` |
Example: for Azure, put the portal "Download usage" export at
`incoming/azure/AzureUsage.csv` and commit it.
## What happens on push
1. `costctl ingest` parses every `*.csv` under `incoming/<provider>/`, sums the
   rows to per-day/per-service totals and merges them into
   `data/spend/<provider>.json` (idempotent — re-dropping the same period just
   updates it).
2. `costctl report` regenerates `web/public/dashboard.json` + the XLSX.
3. Processed raw files are moved to `data/archive/` (git-ignored, so the large
   raw CSVs do not bloat the repo) and the resulting data changes are committed
   back to the branch.
## Run it locally
```bash
go run ./cmd/costctl ingest --dir incoming --data ./data
go run ./cmd/costctl report --data ./data
```
