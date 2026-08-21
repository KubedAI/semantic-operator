#!/usr/bin/env bash
# Shared Postgres (polaris + datahub databases).
. "$(dirname "$0")/lib.sh"

kubectl apply -f "$DEPLOY_DIR/namespaces.yaml"
kubectl apply -f "$DEPLOY_DIR/postgres/postgres.yaml"

log "waiting for postgres rollout"
kubectl -n account-demo rollout status deploy/postgres --timeout=180s

log "verifying databases exist"
dbs=$(kubectl -n account-demo exec deploy/postgres -- \
  psql -U postgres -tAc "SELECT datname FROM pg_database WHERE datname IN ('polaris','datahub') ORDER BY 1" \
  | tr -d '\r' | paste -sd, -)
log "databases present: ${dbs:-<none>}"
[ "$dbs" = "datahub,polaris" ] || die "expected databases datahub,polaris — got '${dbs}' (check init logs: kubectl -n account-demo logs deploy/postgres)"
log "postgres OK"
