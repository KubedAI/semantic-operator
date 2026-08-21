#!/usr/bin/env bash
# Ingest the SaaS accounts Iceberg tables into DataHub, reading
# from the Polaris REST catalog with Garage FileIO. Runs client-side via uv
# (PEP 723 inline deps in datahub_ingest.py). Reads Garage + Polaris credentials
# from the in-cluster Secrets and exposes them (plus the kind host endpoints) as
# the environment the recipe expands.
. "$(dirname "$0")/lib.sh"

command -v uv >/dev/null 2>&1 || die "uv is required (https://docs.astral.sh/uv/)"

d() { kubectl -n account-demo get secret "$1" -o jsonpath="{.data.$2}" | base64 -d; }

# Garage S3 FileIO (host reaches Garage at the kind host mapping localhost:3900).
export AWS_ACCESS_KEY_ID="$(d garage-credentials AWS_ACCESS_KEY_ID)"
export AWS_SECRET_ACCESS_KEY="$(d garage-credentials AWS_SECRET_ACCESS_KEY)"
export AWS_REGION="$(d garage-credentials region)"
export AWS_S3_ENDPOINT="${GARAGE_S3_ENDPOINT:-http://localhost:3900}"
[ -n "$AWS_ACCESS_KEY_ID" ] || die "could not read garage-credentials (is Garage deployed?)"

# Polaris OAuth2 client-credentials (root principal); REST catalog at host 8181.
PID="$(d polaris-credentials ROOT_CLIENT_ID)"
PSEC="$(d polaris-credentials ROOT_CLIENT_SECRET)"
[ -n "$PSEC" ] || die "could not read polaris-credentials (is Polaris deployed?)"
export ICEBERG_CREDENTIAL="${PID}:${PSEC}"
export ICEBERG_REST_URI="${ICEBERG_REST_URI:-http://localhost:8181/api/catalog}"
export ICEBERG_WAREHOUSE="${ICEBERG_WAREHOUSE:-account-demo}"

# DataHub GMS sink (host 8080). Token empty when GMS auth is disabled (dev default).
export DATAHUB_GMS_URL="${DATAHUB_GMS_URL:-http://localhost:8080}"
export DATAHUB_GMS_TOKEN="${DATAHUB_GMS_TOKEN:-}"

log "ingesting Iceberg tables from $ICEBERG_REST_URI (warehouse $ICEBERG_WAREHOUSE) -> $DATAHUB_GMS_URL"
uv run "$LOCAL_DIR/scripts/datahub_ingest.py" "$DEPLOY_DIR/datahub/iceberg-recipe.yaml"
log "ingestion complete — verify datasets on the iceberg platform in the DataHub UI (localhost:9002)"
