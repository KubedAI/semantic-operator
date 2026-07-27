#!/usr/bin/env bash
# Generate and load the small (~44k-row) customer-health dataset into the
# iceberg catalog through StarRocks. Uses the FE NodePort (localhost:9030), so
# no port-forward is needed. The generator is a standalone Go module in
# local/data-gen (own go.mod, only the MySQL driver as a dependency).
. "$(dirname "$0")/lib.sh"

SR_HOST="${SR_HOST:-127.0.0.1}"
SR_PORT="${SR_PORT:-9030}"

log "loading small dataset into iceberg.saas_customer_health_demo via $SR_HOST:$SR_PORT"
log "this can take a few minutes — StarRocks commits each table to Iceberg as it loads"
( cd "$LOCAL_DIR/data-gen" && CGO_ENABLED=0 go run . \
    --host "$SR_HOST" --port "$SR_PORT" --user root --catalog iceberg )
