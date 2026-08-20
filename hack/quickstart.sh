#!/usr/bin/env bash
# Stand up the minimal local stack for the quickstart: a plaintext Trino, the
# operator and server with header auth and no external providers, the retail
# demo data, and the plain retail model. No TLS, no passwords, no Keycloak, and
# no OPA or Ranger. The governance and identity examples add those.
set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-semantic-operator-dev}"
KUBECONFIG_PATH="${KIND_KUBECONFIG:-$ROOT_DIR/.kube/config}"
NAMESPACE="${KIND_NAMESPACE:-semantic-system}"
RELEASE_NAME="${KIND_RELEASE_NAME:-semantic-operator}"
IMAGE_BASE="${KIND_IMAGE_BASE:-semantic-operator-kind}"
IMAGE_TAG="${KIND_IMAGE_TAG:-local}"
PLATFORM="${KIND_PLATFORM:-linux/amd64}"
LOCAL_PORT="${KIND_TRINO_LOCAL_PORT:-18080}"

TRINO="$ROOT_DIR/test/e2e/trino/quickstart.yaml"
VALUES="$ROOT_DIR/test/e2e/helm-values/quickstart.yaml"
MODEL="$ROOT_DIR/examples/retail/model/semanticmodel.yaml"
PATCH="$ROOT_DIR/test/e2e/models/trino-quickstart-patch.yaml"

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME")
KNS=("${KUBECTL[@]}" --namespace "$NAMESPACE")
HELM=(helm --kubeconfig "$KUBECONFIG_PATH" --kube-context "kind-$CLUSTER_NAME")
MANAGER_IMAGE="$IMAGE_BASE/manager:$IMAGE_TAG"
SERVER_IMAGE="$IMAGE_BASE/server:$IMAGE_TAG"

# Namespace and the plaintext engine.
"${KUBECTL[@]}" create namespace "$NAMESPACE" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
"${KNS[@]}" apply -f "$TRINO"
"${KNS[@]}" rollout restart deployment/trino
"${KNS[@]}" rollout status deployment/trino --timeout=5m

# Build the images and load them into the kind node.
make -C "$ROOT_DIR" docker-build \
  IMAGE_BASE="$IMAGE_BASE" TAG="$IMAGE_TAG" PLATFORM="$PLATFORM"
kind load docker-image "$MANAGER_IMAGE" "$SERVER_IMAGE" --name "$CLUSTER_NAME"

# Install the operator and server with the minimal values.
"${HELM[@]}" upgrade --install "$RELEASE_NAME" "$ROOT_DIR/charts/semantic-operator" \
  --namespace "$NAMESPACE" \
  --create-namespace \
  --values "$VALUES" \
  --set-string "image.manager.repository=$IMAGE_BASE/manager" \
  --set-string "image.server.repository=$IMAGE_BASE/server" \
  --set-string "image.tag=$IMAGE_TAG" \
  --wait \
  --timeout 5m
"${KNS[@]}" rollout restart \
  deployment/"$RELEASE_NAME"-manager deployment/"$RELEASE_NAME"-server
"${KNS[@]}" rollout status deployment/"$RELEASE_NAME"-server --timeout=2m

# Load the compact retail dataset over a local port-forward to plain HTTP Trino.
"${KNS[@]}" port-forward service/trino "$LOCAL_PORT:8080" --address 127.0.0.1 &
port_forward_pid=$!
trap 'kill "$port_forward_pid" 2>/dev/null || true' EXIT
for _ in $(seq 30); do
  curl -fs "http://127.0.0.1:$LOCAL_PORT/v1/info/state" | grep -q ACTIVE && break
  sleep 1
done

(
  cd "$ROOT_DIR"
  CGO_ENABLED=0 \
  SQL_DIALECT=trino \
  ENGINE_HOST=127.0.0.1 \
  ENGINE_PORT="$LOCAL_PORT" \
  ENGINE_USER=semantic-manager \
  ENGINE_CATALOG=memory \
  DEMO_DATABASE=osi_demo \
    go run ./examples/retail/data -profile e2e
)

# Apply the plain retail model (connection only, no external provider).
"${KNS[@]}" patch --local -f "$MODEL" --type merge --patch-file "$PATCH" -o yaml | \
  "${KNS[@]}" patch --local -f - --type merge \
    --patch "{\"metadata\":{\"namespace\":\"$NAMESPACE\"}}" -o yaml | \
  "${KNS[@]}" apply -f -
"${KNS[@]}" wait --for=condition=Published semanticmodel/tpcds-retail --timeout=5m
"${KNS[@]}" get semanticmodel/tpcds-retail

echo "Quickstart stack ready in namespace $NAMESPACE"
echo "Query it: kubectl -n $NAMESPACE port-forward svc/$RELEASE_NAME-server 8090:8090"
