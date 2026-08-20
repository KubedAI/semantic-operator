#!/usr/bin/env bash
# Load the compact retail dataset and publish the E2E model against the engine
# selected by KIND_ENGINE_TYPE (trino or starrocks). The data load and model
# publish are shared; per engine only the connection, loader dialect, and model
# patch differ.
set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-semantic-operator-dev}"
KUBECONFIG_PATH="${KIND_KUBECONFIG:-$ROOT_DIR/.kube/config}"
NAMESPACE="${KIND_NAMESPACE:-semantic-system}"
ENGINE_TYPE="${KIND_ENGINE_TYPE:-trino}"
MODEL="$ROOT_DIR/examples/retail/model/semanticmodel.yaml"

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME" --namespace "$NAMESPACE")

case "$ENGINE_TYPE" in
trino)
  ENGINE_DEPLOY=trino
  LOCAL_PORT="${KIND_TRINO_LOCAL_PORT:-18080}"
  REMOTE_PORT=8443
  PATCH="$ROOT_DIR/test/e2e/models/trino-opa-patch.yaml"
  # The Trino E2E authorizes through OPA.
  "${KUBECTL[@]}" rollout status deployment/opa --timeout=2m
  # Trino speaks HTTPS on the MySQL-less protocol; the operator credential is
  # sent as Basic auth over TLS.
  engine_env=(SQL_DIALECT=trino ENGINE_CATALOG=memory
    ENGINE_TLS_ENABLED=true ENGINE_TLS_INSECURE_SKIP_VERIFY=true)
  ready() { curl -fsk "https://127.0.0.1:$LOCAL_PORT/v1/info/state" | grep -q ACTIVE; }
  ;;
starrocks)
  ENGINE_DEPLOY=starrocks
  LOCAL_PORT="${KIND_STARROCKS_LOCAL_PORT:-19030}"
  REMOTE_PORT=9030
  PATCH="$ROOT_DIR/test/e2e/models/starrocks-patch.yaml"
  engine_env=(SQL_DIALECT=starrocks ENGINE_CATALOG=default_catalog)
  # rollout status already gated FE query-readiness; there is no host mysql
  # client, so a live port-forward tunnel is enough to proceed.
  ready() { (echo >"/dev/tcp/127.0.0.1/$LOCAL_PORT") 2>/dev/null; }
  ;;
*)
  echo "unsupported KIND_ENGINE_TYPE=$ENGINE_TYPE (want trino or starrocks)" >&2
  exit 1
  ;;
esac

"${KUBECTL[@]}" rollout status "deployment/$ENGINE_DEPLOY" --timeout=5m

# Load the data through a local port-forward to the engine.
"${KUBECTL[@]}" port-forward "service/$ENGINE_DEPLOY" "$LOCAL_PORT:$REMOTE_PORT" --address 127.0.0.1 &
port_forward_pid=$!
trap 'kill "$port_forward_pid" 2>/dev/null || true' EXIT

for _ in $(seq 30); do
  ready && break
  sleep 1
done

(
  cd "$ROOT_DIR"
  env CGO_ENABLED=0 \
    ENGINE_HOST=127.0.0.1 ENGINE_PORT="$LOCAL_PORT" \
    ENGINE_USER=semantic-manager ENGINE_PASSWORD=manager \
    DEMO_DATABASE=osi_demo \
    "${engine_env[@]}" \
    go run ./examples/retail/data -profile e2e
)

# Merge the engine's connection (and, for Trino, provider) settings onto the
# shared model without changing it, then apply into the release namespace.
"${KUBECTL[@]}" patch --local -f "$MODEL" --type merge --patch-file "$PATCH" -o yaml | \
  "${KUBECTL[@]}" patch --local -f - --type merge \
    --patch "{\"metadata\":{\"namespace\":\"$NAMESPACE\"}}" -o yaml | \
  "${KUBECTL[@]}" apply -f -
"${KUBECTL[@]}" wait --for=condition=Published semanticmodel/tpcds-retail --timeout=5m
"${KUBECTL[@]}" wait --for=condition=ViewsReady semanticmodel/tpcds-retail --timeout=5m
"${KUBECTL[@]}" get semanticmodel/tpcds-retail
