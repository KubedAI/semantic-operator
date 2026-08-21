#!/usr/bin/env bash
# Install DataHub and its prerequisites (OpenSearch + Kafka)
# into the 'datahub' namespace, backed by the shared Postgres 'datahub' DB.
# Prereqs come up first (DataHub's system-update job needs OpenSearch + Kafka
# ready), then the DataHub chart, then the NodePorts. Everything is single
# replica / minimal resources (see deploy/datahub/*.yaml). Images must already
# be loaded (make charts-images images-pull images-load).
. "$(dirname "$0")/lib.sh"

for command in base64 curl helm kubectl openssl uv; do
  command -v "$command" >/dev/null 2>&1 || die "required command not found: $command"
done

CHARTS="$DEPLOY_DIR/datahub/charts"
DHV="$DEPLOY_DIR/datahub"
PREREQ_TGZ="$CHARTS/datahub-prerequisites-${DATAHUB_PREREQ_CHART_VERSION}.tgz"
DH_TGZ="$CHARTS/datahub-${DATAHUB_HELM_CHART_VERSION}.tgz"
KEYCLOAK_CREDENTIAL_FILE="$LOCAL_DIR/.tmp/keycloak/demo-credentials.env"
TRUST_CA="$LOCAL_DIR/.tmp/tls/trust-ca.crt"
[ -f "$PREREQ_TGZ" ] && [ -f "$DH_TGZ" ] || die "vendored charts missing; run 'make charts-vendor'"
[ -r "$KEYCLOAK_CREDENTIAL_FILE" ] || die "Keycloak credentials missing; run 'make keycloak-up' first"
[ -r "$TRUST_CA" ] || die "gateway public CA missing; run 'make gateway-up' first"
openssl x509 -in "$TRUST_CA" -noout -checkend 0 >/dev/null || die "gateway public CA is invalid or expired"

# Values in this generated file are validated as hex before use. Only the
# DataHub client secret is mirrored into the datahub namespace.
# shellcheck disable=SC1090
. "$KEYCLOAK_CREDENTIAL_FILE"
[ -n "${DATAHUB_CLIENT_SECRET:-}" ] || die "missing DATAHUB_CLIENT_SECRET in $KEYCLOAK_CREDENTIAL_FILE"
[[ "$DATAHUB_CLIENT_SECRET" =~ ^[a-f0-9]+$ ]] || die "invalid non-hex DATAHUB_CLIENT_SECRET"

issuer="$(curl --silent --show-error --fail --noproxy '*' \
  --resolve auth.localtest.me:8443:127.0.0.1 \
  --cacert "$TRUST_CA" \
  https://auth.localtest.me:8443/realms/saas-accounts/.well-known/openid-configuration \
  | uv run "$LOCAL_DIR/scripts/json_value.py" issuer)"
[ "$issuer" = "https://auth.localtest.me:8443/realms/saas-accounts" ] || \
  die "unexpected Keycloak issuer: ${issuer:-<empty>}"

log "namespace datahub"
kubectl apply -f "$DEPLOY_DIR/namespaces.yaml"

log "mirroring DataHub database and OIDC credentials into namespace datahub"
PW="$(kubectl -n account-demo get secret postgres-credentials -o jsonpath='{.data.DATAHUB_DB_PASSWORD}' | base64 -d)"
[ -n "$PW" ] || die "could not read DATAHUB_DB_PASSWORD from account-demo/postgres-credentials (is Postgres deployed?)"
kubectl -n datahub create secret generic datahub-postgres \
  --from-literal=postgres-password="$PW" \
  --dry-run=client -o yaml | kubectl apply -f -
printf '%s' "$DATAHUB_CLIENT_SECRET" \
  | kubectl -n datahub create secret generic datahub-oidc \
      --from-file=client-secret=/dev/stdin --dry-run=client -o yaml \
  | kubectl apply -f -
kubectl -n datahub create configmap gateway-ca \
  --from-file=ca.crt="$TRUST_CA" --dry-run=client -o yaml \
  | kubectl apply -f -

oidc_hash="$({ cat "$DHV/values.yaml" "$TRUST_CA"; printf '%s' "$DATAHUB_CLIENT_SECRET"; } \
  | openssl dgst -sha256 | awk '{print $NF}')"

log "note: this installs OpenSearch, Kafka, and DataHub with --wait; expect 5-10 minutes"

log "installing prerequisites (OpenSearch) — release name MUST be 'prerequisites'"
helm upgrade --install prerequisites "$PREREQ_TGZ" \
  --namespace datahub -f "$DHV/prereqs-values.yaml" --wait --timeout 12m

log "deploying single-node Apache Kafka (KRaft) as prerequisites-kafka"
kubectl apply -f "$DHV/kafka.yaml"
kubectl -n datahub rollout status statefulset/prerequisites-kafka --timeout=300s

log "installing DataHub (GMS + OIDC frontend); the system-update job + rollout take several minutes — please wait"
helm upgrade --install datahub "$DH_TGZ" \
  --namespace datahub -f "$DHV/values.yaml" --wait --timeout 20m

log "exposing GMS (localhost:8080) and frontend (localhost:9002) on node ports"
kubectl apply -f "$DHV/nodeports.yaml"

# Secret and ConfigMap data do not change the Helm pod template. This non-secret
# hash makes credential, CA, and OIDC value updates restart the frontend.
kubectl -n datahub patch deployment datahub-datahub-frontend --type=merge \
  -p "{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"saas-accounts-demo.local/datahub-oidc-sha256\":\"$oidc_hash\"}}}}}"

kubectl -n datahub rollout status deploy/datahub-datahub-gms --timeout=300s
kubectl -n datahub rollout status deploy/datahub-datahub-frontend --timeout=300s
log "pods:"; kubectl -n datahub get pods
log "DataHub ready — SSO UI https://datahub.localtest.me:8443  GMS http://localhost:8080"
log "next: datahub-ingest (Iceberg REST -> GMS), then datahub-enrich"
