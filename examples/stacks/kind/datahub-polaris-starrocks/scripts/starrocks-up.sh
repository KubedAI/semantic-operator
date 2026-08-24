#!/usr/bin/env bash
# Deploy one all-in-one StarRocks instance for the local kind demo.
. "$(dirname "$0")/lib.sh"

MANIFEST="$DEPLOY_DIR/starrocks/resources.yaml"
[ -f "$MANIFEST" ] || die "StarRocks manifest not found: $MANIFEST"
[ -n "${STARROCKS_IMAGE:-}" ] || die "STARROCKS_IMAGE is not set in deploy/versions.lock"

# Remove the previous kube-starrocks release. Delete its custom resource while
# the operator is still running, so the operator can remove its workloads.
if kubectl -n account-demo get starrockscluster account-demo >/dev/null 2>&1; then
  log "removing the legacy StarRocksCluster"
  kubectl -n account-demo delete starrockscluster account-demo --wait --timeout=5m
fi
if helm -n account-demo status starrocks >/dev/null 2>&1; then
  log "removing the legacy kube-starrocks Helm release"
  helm -n account-demo uninstall starrocks --wait --timeout 5m
fi
kubectl -n account-demo delete service account-demo-fe-nodeport --ignore-not-found

kubectl create namespace account-demo --dry-run=client -o yaml | kubectl apply -f -
log "deploying all-in-one StarRocks ($STARROCKS_IMAGE)"
sed "s|STARROCKS_IMAGE_REF|$STARROCKS_IMAGE|g" "$MANIFEST" | kubectl apply -f -

# The FE must start and register the BE before queries can run. The readiness
# probe checks that registration.
log "waiting for the StarRocks backend"
kubectl -n account-demo rollout status deployment/starrocks --timeout=10m
kubectl -n account-demo get pod -l app=starrocks -o wide

log "StarRocks OK: in-cluster starrocks:9030, host localhost:9030"
log "next: starrocks-catalog — Iceberg REST external catalog -> Polaris"
