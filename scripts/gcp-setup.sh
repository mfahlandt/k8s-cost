#!/usr/bin/env bash
# Creates a service account + JSON key for the k8s-cost BigQuery collector.
#
# Prereqs: gcloud installed (~/google-cloud-sdk) and you are logged in:
#   gcloud auth login
#
# Usage:
#   scripts/gcp-setup.sh <BILLING_PROJECT_ID> [SA_NAME] [KEY_PATH]
#
# Example:
#   scripts/gcp-setup.sh my-billing-project k8s-cost ./gcp-key.json
set -euo pipefail

PROJECT="${1:?Usage: gcp-setup.sh <BILLING_PROJECT_ID> [SA_NAME] [KEY_PATH]}"
SA_NAME="${2:-k8s-cost}"
KEY_PATH="${3:-./gcp-key.json}"
SA_EMAIL="${SA_NAME}@${PROJECT}.iam.gserviceaccount.com"

echo ">> Using project: $PROJECT"
gcloud config set project "$PROJECT"

echo ">> Enabling BigQuery API"
gcloud services enable bigquery.googleapis.com

echo ">> Creating service account: $SA_EMAIL (ignore error if it exists)"
gcloud iam service-accounts create "$SA_NAME" \
  --display-name="k8s-cost BigQuery collector" || true

echo ">> Granting BigQuery Job User on the billing project"
gcloud projects add-iam-policy-binding "$PROJECT" \
  --member="serviceAccount:${SA_EMAIL}" \
  --role="roles/bigquery.jobUser" \
  --condition=None >/dev/null

echo ">> Creating key: $KEY_PATH"
gcloud iam service-accounts keys create "$KEY_PATH" \
  --iam-account="$SA_EMAIL"

cat <<EOF

Done. Service account: ${SA_EMAIL}
Key written to: ${KEY_PATH}

NEXT: grant read access to the k8s-infra billing dataset. bigquery.jobUser only
lets the SA *run* queries in your project — it still needs to *read* the export
table. A k8s-infra billing admin must share the dataset with:
    ${SA_EMAIL}   (role: BigQuery Data Viewer)

Then use it:
    export GOOGLE_APPLICATION_CREDENTIALS="\$(realpath ${KEY_PATH})"
    export GOOGLE_CLOUD_PROJECT="${PROJECT}"
    go run ./cmd/costctl collect-gcp \\
      --project "\$GOOGLE_CLOUD_PROJECT" \\
      --table kubernetes-public.kubernetes_public_billing.gcp_billing_export_resource_v1_018801_93540E_22A20E \\
      --period 2025-05 --location US
EOF

