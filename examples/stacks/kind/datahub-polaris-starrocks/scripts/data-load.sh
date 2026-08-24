#!/usr/bin/env bash
# Generate and load the small (~44k-row) SaaS accounts dataset into the
# Iceberg catalog through StarRocks. The script uses the FE NodePort at
# localhost:9030, so it does not need a port forward. uv reads the Python
# version and pinned dependency from data-gen/main.py.
. "$(dirname "$0")/lib.sh"

SR_HOST="${SR_HOST:-127.0.0.1}"
SR_PORT="${SR_PORT:-9030}"

log "loading small dataset into iceberg.saas_accounts_demo via $SR_HOST:$SR_PORT"
log "this can take a few minutes — StarRocks commits each table to Iceberg as it loads"
uv run "$LOCAL_DIR/data-gen/main.py" \
    --host "$SR_HOST" --port "$SR_PORT" --user root --catalog iceberg
