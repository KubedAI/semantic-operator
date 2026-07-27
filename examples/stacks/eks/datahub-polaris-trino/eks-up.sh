#!/usr/bin/env bash
# Bring up Polaris (Iceberg REST catalog) on EKS: generated credentials,
# Postgres metastore, Polaris server, and the 'demo' catalog on real S3.
#
# Prereqs (one-time, done outside this script):
#   - S3 bucket for the warehouse (BUCKET below)
#   - IAM role for Polaris bound via EKS Pod Identity to chd/polaris-sa
#     with read/write on the bucket (roles only; no IAM users)
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
NS=chd
CATALOG="${POLARIS_CATALOG:-demo}"
REGION="${AWS_REGION:-us-west-2}"

log() { printf '\033[1;32m[eks-up]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[eks-up]\033[0m %s\n' "$*" >&2; exit 1; }

# Bucket names are globally unique, so the default is derived from the account
# you are actually signed in to rather than hardcoded. Set POLARIS_BUCKET to
# use a bucket you already have.
if [[ -z "${POLARIS_BUCKET:-}" ]]; then
  ACCOUNT="$(aws sts get-caller-identity --query Account --output text)" \
    || die "cannot resolve the AWS account. Sign in, or set POLARIS_BUCKET."
  POLARIS_BUCKET="polaris-demo-warehouse-${ACCOUNT}"
fi
BUCKET="$POLARIS_BUCKET"
BASE="s3://${BUCKET}/${CATALOG}"

kubectl get ns "$NS" >/dev/null 2>&1 || kubectl create ns "$NS"

# Secrets: generate once, reuse on re-runs. Values never touch the repo.
if ! kubectl -n "$NS" get secret postgres-credentials >/dev/null 2>&1; then
  kubectl -n "$NS" create secret generic postgres-credentials \
    --from-literal=POSTGRES_PASSWORD="$(openssl rand -hex 16)" \
    --from-literal=POLARIS_DB_PASSWORD="$(openssl rand -hex 16)"
  log "created postgres-credentials"
fi
if ! kubectl -n "$NS" get secret polaris-credentials >/dev/null 2>&1; then
  kubectl -n "$NS" create secret generic polaris-credentials \
    --from-literal=ROOT_CLIENT_ID=root \
    --from-literal=ROOT_CLIENT_SECRET="$(openssl rand -hex 24)"
  log "created polaris-credentials"
fi

kubectl apply -f "$DIR/postgres.yaml"
kubectl -n "$NS" rollout status deploy/postgres --timeout=180s

kubectl apply -f "$DIR/polaris.yaml"
log "waiting for bootstrap job (creates realm POLARIS + root principal)"
if ! kubectl -n "$NS" wait --for=condition=complete job/polaris-bootstrap --timeout=240s; then
  if kubectl -n "$NS" logs job/polaris-bootstrap 2>/dev/null | grep -qi 'already'; then
    log "realm already bootstrapped"
  else
    kubectl -n "$NS" logs job/polaris-bootstrap 2>&1 | tail -20
    die "bootstrap job did not complete"
  fi
fi
kubectl -n "$NS" rollout status deploy/polaris --timeout=240s

# Create the catalog on real S3. stsUnavailable=true: Polaris does not vend
# credentials; every engine brings its own identity (Pod Identity), which is
# the least-privilege posture we want on EKS.
SECRET="$(kubectl -n "$NS" get secret polaris-credentials -o jsonpath='{.data.ROOT_CLIENT_SECRET}' | base64 -d)"
[ -n "$SECRET" ] || die "polaris root secret not found"

log "creating catalog '$CATALOG' (base $BASE)"
kubectl -n "$NS" exec -i deploy/polaris -- bash -s -- "$SECRET" "$CATALOG" "$BASE" "$REGION" <<'POD'
set -eu
SECRET="$1"; CATALOG="$2"; BASE="$3"; REGION="$4"
API=http://localhost:8181
ACCESS=$(curl -s -X POST "$API/api/catalog/v1/oauth/tokens" -H 'Polaris-Realm: POLARIS' \
  -d grant_type=client_credentials -d client_id=root -d "client_secret=$SECRET" -d scope=PRINCIPAL_ROLE:ALL \
  | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
[ -n "$ACCESS" ] || { echo "TOKEN_FAIL"; exit 1; }
code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $ACCESS" -H 'Polaris-Realm: POLARIS' \
  "$API/api/management/v1/catalogs/$CATALOG")
if [ "$code" = 200 ]; then echo "CATALOG_EXISTS"; exit 0; fi
BODY=$(printf '{"catalog":{"type":"INTERNAL","name":"%s","properties":{"default-base-location":"%s"},"storageConfigInfo":{"storageType":"S3","allowedLocations":["%s"],"stsUnavailable":true,"region":"%s"}}}' \
  "$CATALOG" "$BASE" "$BASE" "$REGION")
code=$(curl -s -o /tmp/cr -w '%{http_code}' -X POST "$API/api/management/v1/catalogs" \
  -H "Authorization: Bearer $ACCESS" -H 'Polaris-Realm: POLARIS' -H 'Content-Type: application/json' -d "$BODY")
echo "CREATE_HTTP=$code"; head -c 500 /tmp/cr; echo
[ "$code" = 201 ] || [ "$code" = 200 ]
POD
log "polaris up: realm POLARIS, catalog '$CATALOG' on $BASE"
