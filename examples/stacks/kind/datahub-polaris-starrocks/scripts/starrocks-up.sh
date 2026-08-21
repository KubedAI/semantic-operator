#!/usr/bin/env bash
# StarRocks (1 FE + 1 CN, shared-data) via kube-starrocks chart.
# The operator (helm-managed) comes up first; it then creates the FE/CN
# StatefulSets from the StarRocksCluster CR, so we poll the CR phase.
. "$(dirname "$0")/lib.sh"

CHART="$(ls "$DEPLOY_DIR"/starrocks/charts/kube-starrocks-*.tgz 2>/dev/null | head -1)"
[ -n "$CHART" ] && [ -f "$CHART" ] || die "kube-starrocks chart not vendored under $DEPLOY_DIR/starrocks/charts (run 'make charts-vendor')"

log "installing kube-starrocks (operator + 1 FE + 1 CN, shared-data on Garage)"
helm upgrade --install starrocks "$CHART" -n account-demo --create-namespace \
  -f "$DEPLOY_DIR/starrocks/values.yaml" --wait --timeout 5m

log "waiting for StarRocksCluster 'account-demo' to reach phase=running (FE+CN created by operator)"
for i in $(seq 1 60); do
  phase="$(kubectl -n account-demo get starrockscluster account-demo -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  printf '  [%02d] phase=%s\n' "$i" "${phase:-<none>}"
  [ "$phase" = "running" ] && break
  sleep 10
done
[ "$phase" = "running" ] || die "StarRocksCluster did not reach running; inspect: kubectl -n account-demo get pods; kubectl -n account-demo describe starrockscluster account-demo"

log "pods:"; kubectl -n account-demo get pods -o wide | grep -E 'account-demo-fe|account-demo-cn' || kubectl -n account-demo get pods

log "exposing FE on node ports (localhost:9030 MySQL / :8030 HTTP)"
kubectl apply -f "$DEPLOY_DIR/starrocks/fe-nodeport.yaml"

log "StarRocks OK — FE MySQL on account-demo-fe-service:9030 (host: localhost:9030 via NodePort mapping)"
log "next: starrocks-catalog — Iceberg REST external catalog -> Polaris"
