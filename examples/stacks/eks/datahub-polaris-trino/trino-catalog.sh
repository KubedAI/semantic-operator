#!/usr/bin/env bash
# Stage 2: wire the existing Trino to Polaris as catalog 'polaris'.
# Idempotent. The OAuth credential reaches Trino via ${ENV:...} from a
# Kubernetes Secret; it never appears in a ConfigMap.
set -euo pipefail
NS_TRINO="${NS_TRINO:-trino}"
NS_POLARIS="${NS_POLARIS:-chd}"
CATALOG="${POLARIS_CATALOG:-demo}"
POLARIS_URI="http://polaris.${NS_POLARIS}.svc.cluster.local:8181/api/catalog"

log() { printf '\033[1;32m[trino-catalog]\033[0m %s\n' "$*"; }

SECRET=$(kubectl -n "$NS_POLARIS" get secret polaris-credentials -o jsonpath='{.data.ROOT_CLIENT_SECRET}' | base64 -d)
[ -n "$SECRET" ] || { echo "polaris-credentials not found; run eks-up.sh first" >&2; exit 1; }

kubectl -n "$NS_TRINO" create secret generic trino-polaris-credentials \
  --from-literal=POLARIS_OAUTH2_CREDENTIAL="root:${SECRET}" \
  --dry-run=client -o yaml | kubectl apply -f -

log "adding polaris.properties to the trino-catalog ConfigMap"
kubectl -n "$NS_TRINO" patch cm trino-catalog --type=merge -p "{\"data\":{\"polaris.properties\":\"connector.name=iceberg\niceberg.catalog.type=rest\niceberg.rest-catalog.uri=${POLARIS_URI}\niceberg.rest-catalog.warehouse=${CATALOG}\niceberg.rest-catalog.security=OAUTH2\niceberg.rest-catalog.oauth2.credential=\${ENV:POLARIS_OAUTH2_CREDENTIAL}\niceberg.rest-catalog.oauth2.scope=PRINCIPAL_ROLE:ALL\niceberg.rest-catalog.oauth2.server-uri=${POLARIS_URI}/v1/oauth/tokens\niceberg.rest-catalog.vended-credentials-enabled=false\niceberg.file-format=PARQUET\nfs.native-s3.enabled=true\ns3.region=us-west-2\n\"}}"

log "exposing the credential to both Trino deployments"
kubectl -n "$NS_TRINO" set env deploy/trino-coordinator --containers=trino-coordinator \
  --from=secret/trino-polaris-credentials
kubectl -n "$NS_TRINO" set env deploy/trino-worker --containers=trino-worker \
  --from=secret/trino-polaris-credentials

kubectl -n "$NS_TRINO" rollout status deploy/trino-coordinator --timeout=300s
kubectl -n "$NS_TRINO" rollout status deploy/trino-worker --timeout=300s

log "verify: SHOW CATALOGS must list 'polaris'"
POD=$(kubectl -n "$NS_TRINO" get pods -o name | grep coordinator | head -1 | cut -d/ -f2)
kubectl -n "$NS_TRINO" exec "$POD" -c trino-coordinator -- trino --execute "SHOW CATALOGS" 2>/dev/null
