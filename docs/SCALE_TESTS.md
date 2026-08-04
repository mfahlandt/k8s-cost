# Scale-test cost breakdown (AWS + GCP)

How to answer questions like *"what do the upstream 5k-node scale tests cost, per
resource type?"* with `costctl query-aws` / `costctl query-gcp`. Both commands are
**ad-hoc**: they print a table (or write CSV) and never touch `data/`.

## What the numbers include (discounts & credits)

| | GCP (`query-gcp`) | AWS (`query-aws`) |
|---|---|---|
| Metric | `cost + CUD credits + other savings`, `tax`/`adjustment` excluded | `UnblendedCost`, `RECORD_TYPE` Refund/Credit excluded |
| Committed-use / RI / Savings-Plan discounts | **included** (net) | none exist in these accounts |
| Sustained-use + other discounts | **included** (net) | n/a |
| Donated credits (CNCF/provider) | **not** deducted | **not** deducted |

**GCP numbers are net of discounts.** Gross list cost vs. the reported net for
July 2026:

| Job | gross (`cost`) | net (reported) | discount |
|-----|----------------|----------------|----------|
| `pull-kubernetes-gce-master-scale-performance-5000-experimental` | $11,967.94 | $8,719.00 | −27.1% |
| `ci-kubernetes-e2e-gce-scale-performance-5000` | $8,948.10 | $6,445.17 | −28.0% |
| `ci-kubernetes-e2e-gce-scale-performance-5000-experimental` | $5,557.77 | $4,018.36 | −27.7% |
| `pull-kubernetes-gce-master-scale-performance-5000` | $4,675.56 | $3,413.55 | −27.0% |
| `ci-kubernetes-e2e-gce-scale-performance-100` | $4,241.18 | $2,337.34 | −44.9% |

(`--list-labels` / `--list-label-values` report the **gross** `cost` — they are
meant for discovery, not for reporting.)

**AWS numbers are effectively list price.** All five CE metrics are identical for
the scale accounts, i.e. there are no reserved instances, Savings Plans or EDP
discounts in play:

```
UnblendedCost     19,136.5794
AmortizedCost     19,136.5794
NetUnblendedCost  19,136.5794
NetAmortizedCost  19,136.5794
BlendedCost       19,136.4438
```

But the accounts are **fully credit funded** — grouping by `RECORD_TYPE` with
`--include-refunds-credits` shows the credits mirroring the charges exactly:

```
Usage    18,388.14
Support     748.44
Credit  -19,136.58
TOTAL         0.00
```

So $19,136.58 is what the scale tests *consume* (and what a third party would
pay), while the project's out-of-pocket cost is $0. For projecting capex, the
list/consumption number is the relevant one.

## TL;DR — the answer for July 2026

**AWS** — the kops/EC2 scale jobs run in two dedicated boskos accounts
(`k8s-infra-e2e-boskos-scale-001` = `226543828060`,
`k8s-infra-e2e-boskos-scale-002` = `405186867737`). Whole month, UnblendedCost:

| Service | July 2026 | share |
|---------|-----------|-------|
| EC2 – Compute | $14,500.04 | 75.8% |
| VPC (public IPv4 addresses) | $1,870.03 | 9.8% |
| EC2 – Other (EBS, regional data transfer, CPU credits) | $1,655.15 | 8.6% |
| AWS Support (allocated) | $748.44 | 3.9% |
| Elastic Load Balancing | $359.25 | 1.9% |
| everything else (KMS, CloudWatch, S3, SQS, …) | ~$3.7 | <0.1% |
| **Total** | **$19,136.58** | |

Top usage types (same window):

| Usage type | Cost |
|------------|------|
| `USE2-BoxUsage:t3a.medium` (hollow-node/kubemark fleet) | $11,569.51 |
| `USE2-BoxUsage:t3.medium` | $2,403.21 |
| `USE2-PublicIPv4:InUseAddress` | $1,870.03 |
| `USE2-EBS:VolumeUsage.gp3` | $802.80 |
| `USE2-EBS:VolumeP-IOPS.io2` (etcd) | $366.54 |
| `USE2-BoxUsage:c8i.24xlarge` (control plane) | $317.95 |
| `USE2-DataTransfer-Regional-Bytes` (EC2) | $273.80 |
| `USE2-DataTransfer-Regional-Bytes` (ELB) | $248.59 |
| `USE2-CPUCredits:t3a` | $186.94 |
| `USE2-LCUUsage` (ELB) | $104.00 |

A representative 2-day window (2026-07-27 → 2026-07-29) totals **$974.81**:
EC2-Compute $776.88 (79.7%), VPC $98.54 (10.1%), EC2-Other $80.00 (8.2%),
ELB $19.04 (2.0%).

**GCP** — the 5k tests run in `k8s-infra-e2e-scale-5k-project`; the smaller scale
jobs use the `k8s-infra-e2e-boskos-scale-NN` pool. July 2026:

| Project | July 2026 |
|---------|-----------|
| `k8s-infra-e2e-boskos-scale-28` | $7,872.20 |
| `k8s-infra-e2e-scale-5k-project` | $6,496.70 |
| `k8s-infra-e2e-boskos-scale-29` | $6,360.49 |
| `k8s-infra-e2e-boskos-scale-30` | $6,063.52 |
| ~30 further `boskos-scale-NN` projects | ~$450–750 each |

SKU breakdown for `k8s-infra-e2e-scale-5k-project` (July 2026, $6,496.70 total):

| SKU | Cost |
|-----|------|
| E2 Instance Core (Americas) | $3,210.86 |
| E2 Instance RAM (Americas) | $1,722.11 |
| Cloud NAT data processing | $451.19 |
| Storage PD capacity | $284.94 |
| C4 Instance Core | $187.41 |
| Cloud Monitoring time series | $147.46 |
| Internal passthrough NLB outbound processing (us-east1) | $121.40 |
| Inter-zone data transfer out | $120.67 |
| C4 Instance RAM | $79.87 |
| PD snapshots | $42.21 |

## ✅ Per-prow-job attribution: works on GCP, not on AWS

**GCP does it today.** The billing export carries a resource label
`prow_k8s_io_job` on the test resources, so cost can be attributed per job
without any extra setup:

```bash
costctl query-gcp --project kubernetes-public \
  --start 2026-07-01 --end 2026-08-01 \
  --label-key prow_k8s_io_job --group-by job
```

Top GCE jobs, July 2026 (total labelled spend $28,415):

| Prow job | July 2026 |
|----------|-----------|
| `pull-kubernetes-gce-master-scale-performance-5000-experimental` | $8,719.00 |
| `ci-kubernetes-e2e-gce-scale-performance-5000` | $6,445.17 |
| `ci-kubernetes-e2e-gce-scale-performance-5000-experimental` | $4,018.36 |
| `pull-kubernetes-gce-master-scale-performance-5000` | $3,413.55 |
| `ci-kubernetes-e2e-gce-scale-performance-100` | $2,337.34 |
| `ci-kubernetes-e2e-gce-scale-performance-100-1-36` | $305.30 |
| `ci-kubernetes-kops-gce-small-scale-kindnet-using-cl2` | $219.16 |

`ci-kubernetes-e2e-gce-scale-performance-5000` runs every other day — 17 runs in
July, i.e. **≈ $379 per 5k-node run** (daily values range $223–$556).

Resource breakdown for that job (July 2026, $6,445.17):

| SKU | Cost | share |
|-----|------|-------|
| E2 Instance Core (Americas) | $3,891.09 | 60.4% |
| E2 Instance RAM (Americas) | $2,086.15 | 32.4% |
| C4 Instance Core (control plane) | $122.61 | 1.9% |
| Internal passthrough NLB outbound processing (us-east1) | $117.88 | 1.8% |
| Inter-zone data transfer out | $103.42 | 1.6% |
| C4 Instance RAM | $52.26 | 0.8% |
| C3 Core + RAM | $43.01 | 0.7% |
| Inter-region / internet egress | $18.87 | 0.3% |

Labels available in the scale projects (`--list-labels`): `prow_k8s_io_job`,
`k8s-io-cluster-name`, `k8s-io-instance-group`, `k8s-io-role-node`,
`k8s-io-role-master`, `k8s-io-role-control-plane`, `k8s-io-etcd-main`,
`k8s-io-etcd-events`, `group`, `subproject`. So a node-vs-control-plane-vs-etcd
split is possible too:

```bash
costctl query-gcp --project kubernetes-public --start 2026-07-01 --end 2026-08-01 \
  --label-key prow_k8s_io_job --label-value ci-kubernetes-e2e-gce-scale-performance-5000 \
  --group-by "label:k8s-io-instance-group" --group-by sku
```

**AWS cannot do it yet.** The equivalent query

```bash
aws ce get-cost-and-usage --filter '{"Tags":{"Key":"prow.k8s.io/job","Values":["..."]}}'
```

returns nothing, because **`prow.k8s.io/job` is not activated as a cost
allocation tag** in the payer account. Only three tags are activated:

```bash
costctl query-aws --profile k8s --start 2026-07-01 --end 2026-08-01 --list-tag-values group
# -> "", sig-cluster-lifecycle, sig-k8s-infra
```

Activated keys: `awsApplication`, `environment`, `group`.

To make per-job queries possible, two things are needed:

1. the test infrastructure must actually **tag** the created resources with
   `prow.k8s.io/job` (kops/CAPA jobs currently do not consistently), and
2. an admin must activate the key in the payer account under **Billing → Cost
   allocation tags** (data only becomes queryable from the activation month
   onwards — it is *not* applied retroactively).

Until then, the **account granularity above is the best available AWS proxy**,
which is fine for scale tests because they run in dedicated boskos accounts.

## Dashboard tab

The dashboard has a **Scale tests** tab (`#/scale`) fed by `web/public/scale.json`:

```bash
costctl collect-scale --project kubernetes-public --profile k8s \
  --start 2026-01-01 --end 2026-08-05
```

It emits three views:

| View | Granularity | History |
|------|-------------|---------|
| `gcp` | per prow job (`prow_k8s_io_job` label) | from **2026-04-28** — the label does not exist before that |
| `gcp-projects` | per scale project (`project.id LIKE '%scale%'`) | full year |
| `aws` | per scale boskos account | full year |

`gcp` and `gcp-projects` describe the *same* money at different granularity, so
only `gcp` + `aws` are added into the headline figure.

Backfilled year to date (2026-01-01 → 2026-08-05):

| Month | GCP scale projects | AWS scale accounts |
|-------|--------------------|--------------------|
| Jan | $84,212 | $15,979 |
| Feb | $48,495 | $19,591 |
| Mar | $38,565 | $19,739 |
| Apr | $42,045 | $20,142 |
| May | $44,174 | $18,036 |
| Jun | $46,280 | $19,045 |
| Jul | $40,623 | $19,137 |
| **YTD** | **$346,598** | **$133,516** |

Per-job totals since the label exists (2026-04-28 → 2026-08-03, $87,476):
`pull-kubernetes-gce-master-scale-performance-5000` $24,436 (31 runs, ~$788/run),
`ci-kubernetes-e2e-gce-scale-performance-5000` $20,412 (48 runs, ~$425/run),
`ci-kubernetes-e2e-gce-scale-resource-size` $13,999, `…-5000-experimental`
$11,246, `ci-kubernetes-e2e-gce-scale-performance-100` $7,274.

The dataset is **incremental**. Raw `(day, group, item, cost)` rows live in
`data/scale/<view>.jsonl`; a run only re-fetches the requested window, replaces
those days and re-aggregates the whole history into `scale.json`. So the yearly
backfill happens **once** (~2 min, one full BigQuery year scan) and the daily
workflow run only touches the current + previous month (~20 s):

```bash
# one-off backfill
costctl collect-scale --project kubernetes-public --profile k8s \
  --start 2026-01-01 --end 2026-08-05

# what CI does daily — previous month .. tomorrow
costctl collect-scale --project kubernetes-public --profile k8s \
  --start 2026-07-01 --end 2026-08-05

# re-render the tab from stored rows without querying anything
costctl collect-scale --rebuild-only
```

Rows are compacted before they are stored: per `(day, group)` only the 8 biggest
resources are kept individually, the sub-cent tail is folded into `Other`, and
zero rows are dropped. Totals and daily series stay exact; this keeps the
committed store at ~3 MB instead of ~48 MB. The project view is grouped by
service (not SKU) for the same reason — the SKU detail lives in the job view.

## Commands

Per-service breakdown for the scale accounts:

```bash
costctl query-aws --profile k8s \
  --start 2026-07-01 --end 2026-08-01 --granularity MONTHLY \
  --filter "LINKED_ACCOUNT=226543828060,405186867737" \
  --group-by SERVICE
```

Service + usage type (EBS, NAT, data transfer, instance families) into CSV:

```bash
costctl query-aws --profile k8s \
  --start 2026-07-01 --end 2026-08-01 --granularity MONTHLY \
  --filter "LINKED_ACCOUNT=226543828060,405186867737" \
  --group-by SERVICE --group-by USAGE_TYPE \
  --csv reports/adhoc/aws-scale-2026-07-service-usagetype.csv
```

Daily series for a single test window:

```bash
costctl query-aws --profile k8s --start 2026-07-27 --end 2026-07-29 \
  --filter "LINKED_ACCOUNT=226543828060,405186867737" --group-by SERVICE
```

Once the tag is activated, the per-job query works like this:

```bash
costctl query-aws --profile k8s --start 2026-07-27 --end 2026-07-29 \
  --tag "prow.k8s.io/job=ci-kubernetes-e2e-kops-aws-scale-amazonvpc-using-cl2" \
  --group-by SERVICE --group-by USAGE_TYPE
```

GCP side (BigQuery billing export, ADC or `GOOGLE_APPLICATION_CREDENTIALS`):

```bash
costctl query-gcp --project kubernetes-public \
  --start 2026-07-01 --end 2026-08-01 --group-by project \
  --csv reports/adhoc/gcp-2026-07-projects.csv

costctl query-gcp --project kubernetes-public \
  --start 2026-07-01 --end 2026-08-01 \
  --projects k8s-infra-e2e-scale-5k-project \
  --group-by service --group-by sku

# which labels exist / which jobs are labelled (the GCP analogue of ce:GetTags)
costctl query-gcp --project kubernetes-public --start 2026-07-01 --end 2026-08-01 \
  --projects k8s-infra-e2e-scale-5k-project --list-labels
costctl query-gcp --project kubernetes-public --start 2026-07-01 --end 2026-08-01 \
  --list-label-values prow_k8s_io_job

# per prow job, and per job + SKU
costctl query-gcp --project kubernetes-public --start 2026-07-01 --end 2026-08-01 \
  --label-key prow_k8s_io_job --group-by job
costctl query-gcp --project kubernetes-public --start 2026-07-01 --end 2026-08-01 \
  --label-key prow_k8s_io_job --label-value ci-kubernetes-e2e-gce-scale-performance-5000 \
  --group-by service --group-by sku
```

`--group-by` accepts `project`, `service`, `sku`, `region`, `job` (shorthand for
`label:prow_k8s_io_job`) and any `label:<key>`. `--label-source` switches between
`labels`, `system_labels` and `project_labels`.

## Required permissions

| Command | Permission |
|---------|------------|
| `query-aws` | `ce:GetCostAndUsage` (payer/management account for org-wide data) |
| `query-aws --list-tag-values` | `ce:GetTags` |
| `query-gcp` | `roles/bigquery.dataViewer` on the billing export + `roles/bigquery.jobUser` |

Cost Explorer API calls are billed at ~$0.01 each; a month of BigQuery billing
export scans ~23 GB (~$0.14).






