#!/usr/bin/env bash
# Deploy a single-node StarRocks (allin1, shared-nothing) into kind with static
# and OIDC/JWT authentication.
#
set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-semantic-operator-dev}"
KUBECONFIG_PATH="${KIND_KUBECONFIG:-$ROOT_DIR/.kube/config}"
NAMESPACE="${KIND_NAMESPACE:-semantic-system}"
IMAGE="${KIND_STARROCKS_IMAGE:-starrocks/allin1-ubuntu:4.0.13}"
TEMPLATE="$ROOT_DIR/test/e2e/starrocks/resources.yaml"
SR_AUDIENCE="${STARROCKS_JWT_AUDIENCE:-starrocks}"
MANAGER_USER="${STARROCKS_MANAGER_USER:-semantic-manager}"
MANAGER_PW="${STARROCKS_MANAGER_PASSWORD:-manager}"
SERVER_USER="${STARROCKS_SERVER_USER:-semantic-server}"
SERVER_PW="${STARROCKS_SERVER_PASSWORD:-server}"
SR_ISSUER="http://keycloak.$NAMESPACE.svc:8080/realms/semantic"
SR_JWKS="$SR_ISSUER/protocol/openid-connect/certs"

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME")
KUBECTL_NS=("${KUBECTL[@]}" --namespace "$NAMESPACE")

"${KUBECTL[@]}" create namespace "$NAMESPACE" --dry-run=client -o yaml |
  "${KUBECTL[@]}" apply -f -

# Operator engine credential Secrets. Their passwords must match the static
# users created in StarRocks below; the names are what the chart's
# engine.<component>.passwordSecret references.
"${KUBECTL_NS[@]}" create secret generic engine-manager-cred \
  --from-literal=password="$MANAGER_PW" --dry-run=client -o yaml | "${KUBECTL_NS[@]}" apply -f -
"${KUBECTL_NS[@]}" create secret generic engine-server-cred \
  --from-literal=password="$SERVER_PW" --dry-run=client -o yaml | "${KUBECTL_NS[@]}" apply -f -

sed "s|STARROCKS_IMAGE_REF|${IMAGE}|" "$TEMPLATE" |
  "${KUBECTL_NS[@]}" apply -f -

# allin1 boots a JVM front end then registers the backend, which is slow on a
# cold node; the readiness probe gates on a live backend, so wait generously.
"${KUBECTL_NS[@]}" rollout status deployment/starrocks --timeout=10m

# --- Authentication: static passwords and JWT together ---
# Native (static) auth is tried first for local users, so root and the two
# operator users below keep working; end users authenticate with a Keycloak JWT
# through the OIDC security integration.

# root is native and passwordless on allin1, so it runs the setup SQL.
sr_sql() { "${KUBECTL_NS[@]}" exec -i deploy/starrocks -- mysql -uroot -h127.0.0.1 -P9030 -N "$@"; }

# Reconcile the OIDC (JWT) integration; ALTER when it exists (there is no
# CREATE OR REPLACE for security integrations), CREATE otherwise.
if sr_sql -e "SHOW SECURITY INTEGRATIONS" 2>/dev/null | grep -qiw oidc; then
  echo "Updating the OIDC (JWT) security integration ..."
  sr_sql <<SQL
ALTER SECURITY INTEGRATION oidc SET (
  "jwks_url" = "$SR_JWKS",
  "principal_field" = "preferred_username",
  "required_issuer" = "$SR_ISSUER",
  "required_audience" = "$SR_AUDIENCE"
);
SQL
else
  echo "Creating the OIDC (JWT) security integration ..."
  sr_sql <<SQL
CREATE SECURITY INTEGRATION oidc PROPERTIES (
  "type" = "authentication_jwt",
  "jwks_url" = "$SR_JWKS",
  "principal_field" = "preferred_username",
  "required_issuer" = "$SR_ISSUER",
  "required_audience" = "$SR_AUDIENCE"
);
SQL
fi

# The server reads under its own identity, so it has an explicit grant. There is
# no blanket grant to public; a public read would override the per-user grants
# that the auth fixture adds after the data load, and bob could not be denied.
sr_sql <<SQL
ADMIN SET FRONTEND CONFIG ("authentication_chain" = "native,oidc");
CREATE USER IF NOT EXISTS '$MANAGER_USER'@'%' IDENTIFIED BY '$MANAGER_PW';
GRANT db_admin TO USER '$MANAGER_USER'@'%';
ALTER USER '$MANAGER_USER'@'%' DEFAULT ROLE 'db_admin';
CREATE USER IF NOT EXISTS '$SERVER_USER'@'%' IDENTIFIED BY '$SERVER_PW';
GRANT SELECT ON ALL TABLES IN ALL DATABASES TO USER '$SERVER_USER'@'%';
SQL

echo "StarRocks is ready at starrocks.$NAMESPACE.svc.cluster.local:9030 (MySQL protocol)"
echo "Static users: $MANAGER_USER / $SERVER_USER (Secrets engine-manager-cred, engine-server-cred)."
echo "JWT: OIDC security integration 'oidc' against the semantic Keycloak realm (audience $SR_AUDIENCE)."
echo "Point the chart at it with:"
echo "  --set engine.type=starrocks --set engine.host=starrocks.$NAMESPACE.svc.cluster.local"
