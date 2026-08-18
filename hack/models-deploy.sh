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

tmp_dir="$(mktemp -d)"
port_forward_pid=""
cleanup() {
  if [[ -n "$port_forward_pid" ]]; then
    kill "$port_forward_pid" >/dev/null 2>&1 || true
    wait "$port_forward_pid" 2>/dev/null || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

"${KUBECTL[@]}" rollout status deployment/trino --timeout=5m
"${KUBECTL[@]}" rollout status deployment/opa --timeout=2m

"${KUBECTL[@]}" port-forward service/trino "$LOCAL_PORT:8080" --address 127.0.0.1 \
  >"$tmp_dir/port-forward.log" 2>&1 &
port_forward_pid=$!

for _ in {1..30}; do
  if curl -fsS "http://127.0.0.1:$LOCAL_PORT/v1/info/state" 2>/dev/null | grep -q ACTIVE; then
    break
  fi
  sleep 1
done
curl -fsS "http://127.0.0.1:$LOCAL_PORT/v1/info/state" | grep -q ACTIVE

(
  cd "$ROOT_DIR"
  CGO_ENABLED=0 \
  SQL_DIALECT=trino \
  ENGINE_HOST=127.0.0.1 \
  ENGINE_PORT="$LOCAL_PORT" \
  ENGINE_CATALOG=memory \
  DEMO_DATABASE=osi_demo \
    go run ./examples/retail/data -profile e2e
)

# Merge the E2E connection/provider settings without changing the shared model.
"${KUBECTL[@]}" patch --local -f "$MODEL" --type merge --patch-file "$PATCH" \
  -o yaml >"$tmp_dir/model-base.yaml"
"${KUBECTL[@]}" patch --local -f "$tmp_dir/model-base.yaml" --type merge \
  --patch "{\"metadata\":{\"namespace\":\"$NAMESPACE\"}}" \
  -o yaml >"$tmp_dir/model.yaml"

"${KUBECTL[@]}" apply -f "$tmp_dir/model.yaml"
"${KUBECTL[@]}" wait --for=condition=Published semanticmodel/tpcds-retail --timeout=5m
"${KUBECTL[@]}" wait --for=condition=ViewsReady semanticmodel/tpcds-retail --timeout=5m
"${KUBECTL[@]}" get semanticmodel/tpcds-retail
