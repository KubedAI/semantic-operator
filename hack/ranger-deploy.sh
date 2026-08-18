#!/usr/bin/env bash
# Deploy and provision a small Ranger Admin/PDP stack.
set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-semantic-operator-dev}"
KUBECONFIG_PATH="${KIND_KUBECONFIG:-$ROOT_DIR/.kube/config}"
NAMESPACE="${KIND_NAMESPACE:-semantic-system}"
ADMIN_IMAGE="${KIND_RANGER_ADMIN_IMAGE:-apache/ranger:2.9.0}"
DB_IMAGE="${KIND_RANGER_DB_IMAGE:-postgres:16}"
PDP_IMAGE="${KIND_RANGER_PDP_IMAGE:-semantic-ranger-pdp:2.9.0}"
PDP_PLATFORM="${KIND_RANGER_PDP_PLATFORM:-linux/amd64}"
ADMIN_LOCAL_PORT="${KIND_RANGER_ADMIN_LOCAL_PORT:-16080}"
RANGER_ASSETS="$ROOT_DIR/test/e2e/ranger"

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME" --namespace "$NAMESPACE")

if ! docker image inspect "$PDP_IMAGE" >/dev/null 2>&1; then
  docker build \
    --platform "$PDP_PLATFORM" \
    --file "$RANGER_ASSETS/Dockerfile.pdp" \
    --tag "$PDP_IMAGE" \
    "$RANGER_ASSETS"
fi

kind load docker-image "$ADMIN_IMAGE" "$DB_IMAGE" "$PDP_IMAGE" --name "$CLUSTER_NAME"

"${KUBECTL[@]}" create configmap ranger-pdp-config \
  --from-file="$RANGER_ASSETS/ranger-pdp-site.xml" \
  --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
"${KUBECTL[@]}" apply -f "$RANGER_ASSETS/resources.yaml"
"${KUBECTL[@]}" set image deployment/ranger-db postgres="$DB_IMAGE"
"${KUBECTL[@]}" set image deployment/ranger-admin configure="$ADMIN_IMAGE" admin="$ADMIN_IMAGE"
"${KUBECTL[@]}" set image deployment/ranger-pdp pdp="$PDP_IMAGE"

"${KUBECTL[@]}" rollout status deployment/ranger-db --timeout=3m
"${KUBECTL[@]}" rollout status deployment/ranger-admin --timeout=8m

"${KUBECTL[@]}" port-forward service/ranger-admin "$ADMIN_LOCAL_PORT:6080" --address 127.0.0.1 &
port_forward_pid=$!
trap 'kill "$port_forward_pid" 2>/dev/null || true' EXIT

RANGER_URL="http://127.0.0.1:$ADMIN_LOCAL_PORT"
for _ in {1..60}; do
  if curl -fsS -u admin:rangerR0cks! "$RANGER_URL/service/plugins/definitions" >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
curl -fsS -u admin:rangerR0cks! "$RANGER_URL/service/plugins/definitions" >/dev/null

if ! curl -fsS -u admin:rangerR0cks! \
  "$RANGER_URL/service/plugins/definitions/name/semantic-operator" >/dev/null 2>&1; then
  curl -fsS -u admin:rangerR0cks! -H 'Content-Type: application/json' \
    --data-binary "@$RANGER_ASSETS/service-def.json" \
    "$RANGER_URL/service/plugins/definitions" >/dev/null
fi
if ! curl -fsS -u admin:rangerR0cks! \
  "$RANGER_URL/service/plugins/services/name/semantic-local" >/dev/null 2>&1; then
  curl -fsS -u admin:rangerR0cks! -H 'Content-Type: application/json' \
    --data-binary "@$RANGER_ASSETS/service.json" \
    "$RANGER_URL/service/plugins/services" >/dev/null
fi
if ! curl -fsS -u admin:rangerR0cks! \
  "$RANGER_URL/service/xusers/users?name=demo-user" | grep -q '"name":"demo-user"'; then
  curl -fsS -u admin:rangerR0cks! -H 'Content-Type: application/json' \
    --data-binary '{"name":"demo-user"}' \
    "$RANGER_URL/service/xusers/users/external" >/dev/null
fi
for role in analyst tx_analyst admin; do
  if ! curl -fsS -u admin:rangerR0cks! \
    "$RANGER_URL/service/public/v2/api/roles/name/$role?serviceName=semantic-local&execUser=admin" \
    >/dev/null 2>&1; then
    printf '{"name":"%s","description":"Semantic local %s role","createdByUser":"admin"}' \
      "$role" "$role" | \
      curl -fsS -u admin:rangerR0cks! -H 'Content-Type: application/json' \
        --data-binary @- \
        "$RANGER_URL/service/public/v2/api/roles?serviceName=semantic-local" >/dev/null
  fi
done
if ! curl -fsS -u admin:rangerR0cks! \
  "$RANGER_URL/service/public/v2/api/service/semantic-local/policy/retail-query" >/dev/null 2>&1; then
  curl -fsS -u admin:rangerR0cks! -H 'Content-Type: application/json' \
    --data-binary "@$RANGER_ASSETS/policy.json" \
    "$RANGER_URL/service/plugins/policies" >/dev/null
fi

"${KUBECTL[@]}" rollout restart deployment/ranger-pdp
"${KUBECTL[@]}" rollout status deployment/ranger-pdp --timeout=5m

echo "Ranger Admin and PDP are ready"
echo "Ranger Admin: kubectl --kubeconfig $KUBECONFIG_PATH --context kind-$CLUSTER_NAME --namespace $NAMESPACE port-forward service/ranger-admin 6080:6080"
