#!/usr/bin/env bash
# Stage 3: load the retail demo data into Polaris by copying the Iceberg
# tables from the existing Glue-backed catalog with CTAS. Idempotent:
# IF NOT EXISTS skips tables that are already loaded.
#
# Prereq: the starrocks/retail demo data exists in iceberg.osi_demo
# (loaded once with `make demo-data`).
set -euo pipefail
NS_TRINO="${NS_TRINO:-trino}"
SCHEMA="${POLARIS_SCHEMA:-osi_demo}"

log() { printf '\033[1;32m[data-load]\033[0m %s\n' "$*"; }

POD=$(kubectl -n "$NS_TRINO" get pods -o name | grep coordinator | head -1 | cut -d/ -f2)
run() { kubectl -n "$NS_TRINO" exec "$POD" -c trino-coordinator -- trino --execute "$1" 2>/dev/null; }

log "creating schema polaris.${SCHEMA}"
run "CREATE SCHEMA IF NOT EXISTS polaris.${SCHEMA}"

for t in store_sales date_dim customer item store; do
  log "copying ${t}"
  run "CREATE TABLE IF NOT EXISTS polaris.${SCHEMA}.${t} AS SELECT * FROM iceberg.osi_demo.${t}"
done

log "verify: row counts in Polaris"
run "SELECT 'store_sales' AS t, count(*) AS rows FROM polaris.${SCHEMA}.store_sales
     UNION ALL SELECT 'date_dim', count(*) FROM polaris.${SCHEMA}.date_dim
     UNION ALL SELECT 'customer', count(*) FROM polaris.${SCHEMA}.customer
     UNION ALL SELECT 'item', count(*) FROM polaris.${SCHEMA}.item
     UNION ALL SELECT 'store', count(*) FROM polaris.${SCHEMA}.store"
