#!/usr/bin/env bash
# Creates the k8s-cost collector service account with the minimal permissions
# needed to query the billing export in BigQuery.
#
# >>> Must be run by a k8s-infra admin with IAM rights on kubernetes-public. <<<
#
# Grants:
#   1. roles/bigquery.jobUser  on project kubernetes-public   (run queries)
#   2. roles/bigquery.dataViewer on dataset kubernetes_public_billing (read export)
#
# Usage:  scripts/gcp-create-sa.sh [key-output-path]
set -euo pipefail

PROJECT="kubernetes-public"
DATASET="kubernetes_public_billing"
SA_NAME="k8s-cost"
SA_EMAIL="${SA_NAME}@${PROJECT}.iam.gserviceaccount.com"
KEY_PATH="${1:-./k8s-cost-sa-key.json}"

echo ">> 1/4 Creating service account ${SA_EMAIL}"
gcloud iam service-accounts create "${SA_NAME}" \
  --project "${PROJECT}" \
  --display-name="k8s-cost billing collector (read-only)" || true

echo ">> 2/4 Granting BigQuery Job User on ${PROJECT}"
gcloud projects add-iam-policy-binding "${PROJECT}" \
  --member="serviceAccount:${SA_EMAIL}" \
  --role="roles/bigquery.jobUser" \
  --condition=None >/dev/null

echo ">> 3/4 Granting BigQuery Data Viewer on dataset ${DATASET}"
bq add-iam-policy-binding \
  --member="serviceAccount:${SA_EMAIL}" \
  --role="roles/bigquery.dataViewer" \
  "${PROJECT}:${DATASET}"

echo ">> 4/4 Creating key: ${KEY_PATH}"
gcloud iam service-accounts keys create "${KEY_PATH}" \
  --iam-account="${SA_EMAIL}"

cat <<EOF

Done.
  Service account: ${SA_EMAIL}
  Key file:        ${KEY_PATH}   ← hand this to the k8s-cost maintainer
                                    (goes into the GCP_SA_KEY GitHub secret)

The SA can ONLY run BigQuery queries billed to ${PROJECT} and read the
billing export dataset — no other access.
EOF

