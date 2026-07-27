#!/usr/bin/env bash
# Apply the local SemanticModels (catalog=iceberg, database
# saas_customer_health_demo) and show reconciliation status. The manager
# validates each model, drift-checks its bindings against the live StarRocks
# iceberg catalog, publishes the compiled artifact, and creates governed views.
. "$(dirname "$0")/lib.sh"

kubectl get namespace semantic-system >/dev/null 2>&1 || die "namespace semantic-system missing; run 'make operator-up' first"

log "applying local SemanticModels from $DEPLOY_DIR/models"
kubectl apply -f "$DEPLOY_DIR/models/"

log "status (want Validated=True, Published=True, Drift=False):"
kubectl -n semantic-system get semanticmodels
log "watch reconciliation: kubectl -n semantic-system get semanticmodels -w"
