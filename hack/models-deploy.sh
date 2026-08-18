#!/usr/bin/env bash
# Load compact retail data and publish the Trino/OPA E2E model.
set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-semantic-operator-dev}"
KUBECONFIG_PATH="${KIND_KUBECONFIG:-$ROOT_DIR/.kube/config}"
NAMESPACE="${KIND_NAMESPACE:-semantic-system}"
LOCAL_PORT="${KIND_TRINO_LOCAL_PORT:-18080}"
MODEL="$ROOT_DIR/examples/retail/model/semanticmodel.yaml"
PATCH="$ROOT_DIR/test/e2e/models/trino-opa-patch.yaml"

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME" --namespace "$NAMESPACE")

"${KUBECTL[@]}" rollout status deployment/trino --timeout=5m
"${KUBECTL[@]}" rollout status deployment/opa --timeout=2m

# Load the data through a local port-forward to the TLS engine.
"${KUBECTL[@]}" port-forward service/trino "$LOCAL_PORT:8443" --address 127.0.0.1 &
port_forward_pid=$!
trap 'kill "$port_forward_pid" 2>/dev/null || true' EXIT

for _ in $(seq 30); do
  curl -fsk "https://127.0.0.1:$LOCAL_PORT/v1/info/state" | grep -q ACTIVE && break
  sleep 1
done

(
  cd "$ROOT_DIR"
  CGO_ENABLED=0 \
  SQL_DIALECT=trino \
  ENGINE_HOST=127.0.0.1 \
  ENGINE_PORT="$LOCAL_PORT" \
  ENGINE_TLS_ENABLED=true \
  ENGINE_TLS_INSECURE_SKIP_VERIFY=true \
  ENGINE_USER=semantic-manager \
  ENGINE_PASSWORD=manager \
  ENGINE_CATALOG=memory \
  DEMO_DATABASE=osi_demo \
    go run ./examples/retail/data -profile e2e
)

# Merge the E2E connection/provider settings without changing the shared model.
"${KUBECTL[@]}" patch --local -f "$MODEL" --type merge --patch-file "$PATCH" -o yaml | \
  "${KUBECTL[@]}" patch --local -f - --type merge \
    --patch "{\"metadata\":{\"namespace\":\"$NAMESPACE\"}}" -o yaml | \
  "${KUBECTL[@]}" apply -f -
"${KUBECTL[@]}" wait --for=condition=Published semanticmodel/tpcds-retail --timeout=5m
"${KUBECTL[@]}" wait --for=condition=ViewsReady semanticmodel/tpcds-retail --timeout=5m
"${KUBECTL[@]}" get semanticmodel/tpcds-retail
