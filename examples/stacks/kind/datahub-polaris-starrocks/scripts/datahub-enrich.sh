#!/usr/bin/env bash
# Enrich the ingested Iceberg datasets with the semantic-operator
# metadata (structured properties, glossary, ownership, PII/confidential tags,
# certification/deprecation, lineage). Runs the enrichment engine client-side
# via uv (PEP 723 inline deps in enrich.py) against the GMS NodePort. Uses the
# local metadata (physical_table bound to the iceberg catalog) and the same
# platform/instance the ingestion emitted.
#
# Extra args pass through, e.g.:  make datahub-enrich ARGS=--dry-run
. "$(dirname "$0")/lib.sh"

command -v uv >/dev/null 2>&1 || die "uv is required (https://docs.astral.sh/uv/)"

export DATAHUB_GMS_URL="${DATAHUB_GMS_URL:-http://localhost:8080}"
# GMS auth is disabled on the dev cluster; a placeholder token satisfies the
# engine's non-empty check and is ignored by GMS. Set a real PAT if you enable
# metadata-service authentication.
export DATAHUB_GMS_TOKEN="${DATAHUB_GMS_TOKEN:-local-no-auth}"

log "enriching iceberg datasets (instance account-demo-local) via $DATAHUB_GMS_URL"
uv run "$LOCAL_DIR/scripts/datahub_enrich.py" \
  --metadata "$DEPLOY_DIR/datahub/enrichment-metadata.yaml" \
  --platform iceberg \
  --platform-instance "${DATAHUB_PLATFORM_INSTANCE:-account-demo-local}" \
  --env DEV \
  --database saas_accounts_demo \
  --manifest-output "$DATA_DIR/datahub-urn-manifest.json" \
  ${ARGS:-} "$@"
log "enrichment complete — manifest at $DATA_DIR/datahub-urn-manifest.json"
