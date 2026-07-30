#!/usr/bin/env bash
# Stage 3: load the retail demo data into Polaris.
#
# Source is Trino's built-in tpcds connector, which generates the data in the
# coordinator. Nothing outside this cluster is read, so the stage works on a
# fresh cluster with no pre-existing warehouse and no extra S3 grant. Trino
# writes the Iceberg files under its own Pod Identity and Polaris tracks the
# metadata.
#
# Idempotent. IF NOT EXISTS skips tables that are already loaded.
set -euo pipefail
NS_TRINO="${NS_TRINO:-trino}"
SCHEMA="${POLARIS_SCHEMA:-osi_demo}"
SF="${TPCDS_SCALE:-sf1}"
Y1="${YEAR_FROM:-2000}"
Y2="${YEAR_TO:-2002}"

log() { printf '\033[1;32m[data-load]\033[0m %s\n' "$*"; }

POD=$(kubectl -n "$NS_TRINO" get pods -o name | grep coordinator | head -1 | cut -d/ -f2)
[ -n "$POD" ] || { echo "no Trino coordinator pod found in namespace $NS_TRINO" >&2; exit 1; }
# The Trino CLI writes a jline "dumb terminal" warning to stderr on every
# non-interactive call. Filter that one line rather than redirecting all of
# stderr, so a real failure is still visible.
run() {
  kubectl -n "$NS_TRINO" exec "$POD" -c trino-coordinator -- trino --execute "$1" \
    2> >(grep -vE 'jline|Unable to create a system terminal|dumb terminal' >&2)
}

log "creating schema polaris.${SCHEMA}"
run "CREATE SCHEMA IF NOT EXISTS polaris.${SCHEMA}" >/dev/null

# Dimensions first, so the fact table's foreign keys always resolve. Every
# table comes from the same scale factor, which is what keeps them consistent.
# tpcds date_dim carries d_moy but not d_month_name, which the certified model
# uses as a dimension. Deriving it here keeps the model and the walkthrough
# unchanged, and a month name reads better in a demo than the number 7.
log "loading date_dim (${Y1}-${Y2})"
run "CREATE TABLE IF NOT EXISTS polaris.${SCHEMA}.date_dim AS
     SELECT *, format_datetime(date_parse(cast(d_moy AS varchar), '%c'), 'MMMM') AS d_month_name
     FROM tpcds.${SF}.date_dim WHERE d_year BETWEEN ${Y1} AND ${Y2}" >/dev/null

for t in item customer store; do
  log "loading ${t}"
  run "CREATE TABLE IF NOT EXISTS polaris.${SCHEMA}.${t} AS SELECT * FROM tpcds.${SF}.${t}" >/dev/null
done

log "loading store_sales (this is the big one, a few minutes)"
run "CREATE TABLE IF NOT EXISTS polaris.${SCHEMA}.store_sales AS
     SELECT ss.* FROM tpcds.${SF}.store_sales ss
     WHERE ss.ss_store_sk IS NOT NULL
       AND ss.ss_item_sk IS NOT NULL
       AND ss.ss_customer_sk IS NOT NULL
       AND ss.ss_sold_date_sk IN (
         SELECT d_date_sk FROM tpcds.${SF}.date_dim WHERE d_year BETWEEN ${Y1} AND ${Y2})" >/dev/null

# Every store in tpcds sf1 sits in one state, so a by-state breakdown would be
# a single row and the tx_analyst row filter would match nothing. Move half of
# them to TX.
#
# Only stores that actually carry sales are eligible. tpcds populates a subset
# of store keys, so picking blindly produces TX stores with no rows, an empty
# governance demo, and a state that never appears in a result.
log "spreading stores with sales across two states, so row filters have an effect"
run "UPDATE polaris.${SCHEMA}.store SET s_state = 'TN'" >/dev/null
TX=$(run "SELECT array_join(array_agg(cast(sk AS varchar)), ',') FROM (
            SELECT sk, row_number() OVER (ORDER BY sk) AS rn
            FROM (SELECT DISTINCT ss_store_sk AS sk FROM polaris.${SCHEMA}.store_sales)
          ) WHERE rn % 2 = 0" | tr -d '"' | tail -1)
[ -n "$TX" ] || { echo "could not determine which stores carry sales" >&2; exit 1; }
log "  TX stores: $TX"
run "UPDATE polaris.${SCHEMA}.store SET s_state = 'TX' WHERE s_store_sk IN (${TX})" >/dev/null

log "verify: row counts in Polaris"
run "SELECT 'store_sales' AS t, count(*) AS rows FROM polaris.${SCHEMA}.store_sales
     UNION ALL SELECT 'date_dim', count(*) FROM polaris.${SCHEMA}.date_dim
     UNION ALL SELECT 'customer', count(*) FROM polaris.${SCHEMA}.customer
     UNION ALL SELECT 'item', count(*) FROM polaris.${SCHEMA}.item
     UNION ALL SELECT 'store', count(*) FROM polaris.${SCHEMA}.store
     ORDER BY 1"

log "verify: stores by state, with sales, so both states return rows"
run "SELECT s.s_state, count(DISTINCT s.s_store_sk) AS stores, count(ss.ss_ticket_number) AS sales
     FROM polaris.${SCHEMA}.store s
     LEFT JOIN polaris.${SCHEMA}.store_sales ss ON ss.ss_store_sk = s.s_store_sk
     GROUP BY s.s_state ORDER BY 1"

log "verify: no orphan foreign keys"
run "SELECT count_if(d.d_date_sk IS NULL) AS orphan_date,
            count_if(s.s_store_sk IS NULL) AS orphan_store
     FROM polaris.${SCHEMA}.store_sales ss
     LEFT JOIN polaris.${SCHEMA}.date_dim d ON d.d_date_sk = ss.ss_sold_date_sk
     LEFT JOIN polaris.${SCHEMA}.store s ON s.s_store_sk = ss.ss_store_sk"
