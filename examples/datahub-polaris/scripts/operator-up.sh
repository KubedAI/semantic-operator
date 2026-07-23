#!/usr/bin/env bash
# Install the semantic-operator (manager) + server into the local
# cluster via the repo Helm chart, then expose the server on the reserved node
# port. Run operator-build.sh first so the images are loaded into the node.
. "$(dirname "$0")/lib.sh"

CHART="$ROOT_DIR/charts/semantic-operator"
[ -d "$CHART" ] || die "helm chart not found at $CHART"

log "installing semantic-operator (manager + server) in namespace semantic-system"
helm upgrade --install semantic-operator "$CHART" \
  --namespace semantic-system --create-namespace \
  -f "$DEPLOY_DIR/operator/values.yaml" --wait --timeout 5m

log "exposing the server on the reserved node port (localhost:8090)"
kubectl apply -f "$DEPLOY_DIR/operator/server-nodeport.yaml"

kubectl -n semantic-system rollout status deploy/semantic-operator-manager --timeout=120s
kubectl -n semantic-system rollout status deploy/semantic-operator-server --timeout=120s
log "pods:"; kubectl -n semantic-system get pods
log "operator + server ready — next: models-apply"
