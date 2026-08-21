#!/usr/bin/env bash
# Polaris bootstrap + server. Catalog creation (S3->Garage) is a
# separate step (polaris-catalog.sh), added after it's validated against Garage.
. "$(dirname "$0")/lib.sh"

kubectl apply -f "$DEPLOY_DIR/namespaces.yaml"
kubectl apply -f "$DEPLOY_DIR/polaris/polaris.yaml"

log "waiting for bootstrap job (creates realm POLARIS + root principal)"
if ! kubectl -n account-demo wait --for=condition=complete job/polaris-bootstrap --timeout=180s; then
  if kubectl -n account-demo logs job/polaris-bootstrap 2>/dev/null | grep -qi 'already'; then
    log "realm already bootstrapped"
  else
    kubectl -n account-demo logs job/polaris-bootstrap 2>&1 | tail -20
    die "bootstrap job did not complete"
  fi
fi

log "waiting for polaris server (readiness gated on /q/health)"
kubectl -n account-demo rollout status deploy/polaris --timeout=240s
log "polaris server OK — catalog API :8181 (host: http://localhost:8181)"
log "next: polaris-catalog creates the Iceberg REST catalog 'account-demo' on Garage"
