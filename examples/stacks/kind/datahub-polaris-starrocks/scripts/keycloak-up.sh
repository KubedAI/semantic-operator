#!/usr/bin/env bash
# Provision Keycloak on shared Postgres and reconcile the local demo realm.
. "$(dirname "$0")/lib.sh"

for command in openssl kubectl curl uv; do
  command -v "$command" >/dev/null 2>&1 || die "required command not found: $command"
done

CREDENTIAL_DIR="$LOCAL_DIR/.tmp/keycloak"
CREDENTIAL_FILE="$CREDENTIAL_DIR/demo-credentials.env"
REALM_FILE="$DEPLOY_DIR/keycloak/saas-accounts-realm.json"
REALM_IMPORT="$DEPLOY_DIR/keycloak/realm-import-job.yaml"
CONFIG_ANNOTATION="saas-accounts-demo.local/keycloak-config-sha256"

umask 077
mkdir -p "$CREDENTIAL_DIR"
chmod 700 "$CREDENTIAL_DIR"
if [ ! -f "$CREDENTIAL_FILE" ]; then
  log "generating stable Keycloak demo credentials under .tmp/keycloak"
  cat >"$CREDENTIAL_FILE" <<EOF
KEYCLOAK_DB_PASSWORD=$(openssl rand -hex 24)
KEYCLOAK_ADMIN_PASSWORD=$(openssl rand -hex 24)
ALICE_PASSWORD=$(openssl rand -hex 16)
BOB_PASSWORD=$(openssl rand -hex 16)
CAROL_PASSWORD=$(openssl rand -hex 16)
DATAHUB_CLIENT_SECRET=$(openssl rand -hex 32)
CHAT_CLIENT_SECRET=$(openssl rand -hex 32)
EOF
fi
chmod 600 "$CREDENTIAL_FILE"
# Values are hex-only, so sourcing this generated file cannot add shell syntax.
# shellcheck disable=SC1090
. "$CREDENTIAL_FILE"
for name in KEYCLOAK_DB_PASSWORD KEYCLOAK_ADMIN_PASSWORD ALICE_PASSWORD BOB_PASSWORD \
  CAROL_PASSWORD DATAHUB_CLIENT_SECRET CHAT_CLIENT_SECRET; do
  [ -n "${!name:-}" ] || die "missing $name in $CREDENTIAL_FILE"
  [[ "${!name}" =~ ^[a-f0-9]+$ ]] || die "invalid non-hex value for $name in $CREDENTIAL_FILE"
done

desired_hash="$(cat "$REALM_FILE" "$CREDENTIAL_FILE" | openssl dgst -sha256 | awk '{print $NF}')"
kubectl apply -f "$DEPLOY_DIR/namespaces.yaml"

existing_deployment=0
existing_deployment_name="$(kubectl -n account-demo get deployment keycloak \
  --ignore-not-found -o name)"
if [ -n "$existing_deployment_name" ]; then
  [ "$existing_deployment_name" = "deployment.apps/keycloak" ] || \
    die "unexpected Keycloak deployment lookup result: $existing_deployment_name"
  existing_deployment=1
fi
realm_config_json="$(kubectl -n account-demo get configmap keycloak-realm \
  --ignore-not-found -o json)"
applied_hash=""
if [ -n "$realm_config_json" ]; then
  applied_hash="$(printf '%s' "$realm_config_json" | \
    uv run "$LOCAL_DIR/scripts/json_value.py" metadata annotations "$CONFIG_ANNOTATION")"
fi

# Stop an existing server before changing credentials or doing an offline import.
if [ "$existing_deployment" -eq 1 ] && [ "$applied_hash" != "$desired_hash" ]; then
  log "stopping Keycloak for configuration reconciliation"
  kubectl -n account-demo scale deployment/keycloak --replicas=0
  remaining_pods=""
  for _ in $(seq 1 90); do
    remaining_pods="$(kubectl -n account-demo get pods \
      -l app.kubernetes.io/name=keycloak -o name)"
    [ -z "$remaining_pods" ] && break
    sleep 2
  done
  remaining_pods="$(kubectl -n account-demo get pods \
    -l app.kubernetes.io/name=keycloak -o name)"
  [ -z "$remaining_pods" ] || \
    die "Keycloak pod did not terminate; refusing concurrent offline import"
fi

kubectl -n account-demo rollout status deployment/postgres --timeout=180s
log "ensuring dedicated Keycloak Postgres role and database"
# Generated passwords are validated as hex above. Send SQL over stdin so secret
# values do not appear in local process arguments or Kubernetes exec commands.
kubectl -n account-demo exec -i deployment/postgres -- \
  psql -v ON_ERROR_STOP=1 -U postgres >/dev/null <<SQL
SELECT format('CREATE ROLE keycloak LOGIN PASSWORD %L', '$KEYCLOAK_DB_PASSWORD')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'keycloak') \gexec
ALTER ROLE keycloak WITH LOGIN PASSWORD '$KEYCLOAK_DB_PASSWORD';
SELECT 'CREATE DATABASE keycloak OWNER keycloak'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'keycloak') \gexec
ALTER DATABASE keycloak OWNER TO keycloak;
SQL

log "creating Keycloak runtime Secret and realm configuration"
# --from-env-file passes only a mode-0600 path in argv; secret values remain in
# stdin between kubectl processes and in the resulting Kubernetes Secret.
kubectl -n account-demo create secret generic keycloak-runtime \
  --from-env-file="$CREDENTIAL_FILE" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n account-demo create configmap keycloak-realm \
  --from-file=saas-accounts-realm.json="$REALM_FILE" \
  --dry-run=client -o yaml | kubectl apply -f -

realm_exists=0
realm_table="$(kubectl -n account-demo exec deployment/postgres -- \
  psql -U postgres -d keycloak -tAc "SELECT COALESCE(to_regclass('public.realm')::text, '')" \
  | tr -d '[:space:]')"
if [ -n "$realm_table" ]; then
  realm_exists="$(kubectl -n account-demo exec deployment/postgres -- \
    psql -U postgres -d keycloak -tAc \
      "SELECT CASE WHEN EXISTS (SELECT 1 FROM realm WHERE name = 'saas-accounts') THEN 1 ELSE 0 END" \
    | tr -d '[:space:]')"
fi

if [ "$realm_exists" = 1 ] && [ "$applied_hash" != "$desired_hash" ]; then
  log "reconciling the persisted SaaS accounts realm offline"
  kubectl -n account-demo delete job keycloak-realm-import --ignore-not-found --wait=true
  kubectl apply -f "$REALM_IMPORT"
  if ! kubectl -n account-demo wait --for=condition=complete \
    job/keycloak-realm-import --timeout=600s; then
    kubectl -n account-demo logs job/keycloak-realm-import >&2 || true
    die "Keycloak realm import failed"
  fi
  kubectl -n account-demo delete job keycloak-realm-import --wait=true
fi

kubectl apply -f "$DEPLOY_DIR/keycloak/keycloak.yaml"
# ConfigMap and Secret volumes do not alter the pod template by themselves.
# A non-secret content hash makes reloads deterministic and no-op when unchanged.
kubectl -n account-demo patch deployment keycloak --type=merge \
  -p "{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"$CONFIG_ANNOTATION\":\"$desired_hash\"}}}}}"
log "waiting for Keycloak"
kubectl -n account-demo rollout status deployment/keycloak --timeout=600s

issuer="$(curl --silent --show-error --fail --noproxy '*' \
  --resolve auth.localtest.me:8443:127.0.0.1 \
  --cacert "$LOCAL_DIR/.tmp/tls/trust-ca.crt" \
  https://auth.localtest.me:8443/realms/saas-accounts/.well-known/openid-configuration \
  | uv run "$LOCAL_DIR/scripts/json_value.py" issuer)"
[ "$issuer" = "https://auth.localtest.me:8443/realms/saas-accounts" ] || \
  die "unexpected Keycloak issuer: ${issuer:-<empty>}"

kubectl -n account-demo annotate configmap keycloak-realm --overwrite \
  "$CONFIG_ANNOTATION=$desired_hash" >/dev/null
log "Keycloak ready at https://auth.localtest.me:8443"
log "demo credentials are mode 0600 at $CREDENTIAL_FILE"
