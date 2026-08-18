#!/usr/bin/env bash
# Build, load, and install the semantic operator and server.
set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-semantic-operator-dev}"
KUBECONFIG_PATH="${KIND_KUBECONFIG:-$ROOT_DIR/.kube/config}"
NAMESPACE="${KIND_NAMESPACE:-semantic-system}"
RELEASE_NAME="${KIND_RELEASE_NAME:-semantic-operator}"
IMAGE_BASE="${KIND_IMAGE_BASE:-semantic-operator-kind}"
IMAGE_TAG="${KIND_IMAGE_TAG:-local}"
PLATFORM="${KIND_PLATFORM:-linux/amd64}"
ENGINE_TYPE="${KIND_ENGINE_TYPE:-trino}"
ENGINE_HOST="${KIND_ENGINE_HOST:-trino.$NAMESPACE.svc.cluster.local}"
ENGINE_PORT="${KIND_ENGINE_PORT:-8080}"

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME" --namespace "$NAMESPACE")
HELM=(helm --kubeconfig "$KUBECONFIG_PATH" --kube-context "kind-$CLUSTER_NAME")
MANAGER_IMAGE="$IMAGE_BASE/manager:$IMAGE_TAG"
SERVER_IMAGE="$IMAGE_BASE/server:$IMAGE_TAG"

if [[ "$ENGINE_TYPE" == trino ]]; then
  "${KUBECTL[@]}" rollout status deployment/trino --timeout=5m
fi

make -C "$ROOT_DIR" docker-build \
  IMAGE_BASE="$IMAGE_BASE" TAG="$IMAGE_TAG" PLATFORM="$PLATFORM"
kind load docker-image "$MANAGER_IMAGE" "$SERVER_IMAGE" --name "$CLUSTER_NAME"

"${HELM[@]}" upgrade --install "$RELEASE_NAME" "$ROOT_DIR/charts/semantic-operator" \
  --namespace "$NAMESPACE" \
  --create-namespace \
  --set-string "image.manager.repository=$IMAGE_BASE/manager" \
  --set-string "image.server.repository=$IMAGE_BASE/server" \
  --set-string "image.tag=$IMAGE_TAG" \
  --set-string "image.pullPolicy=IfNotPresent" \
  --set-string "engine.type=$ENGINE_TYPE" \
  --set-string "engine.host=$ENGINE_HOST" \
  --set "engine.port=$ENGINE_PORT" \
  --set "server.auth.allowInsecureHeaderAuth=true" \
  --set "server.replicas=1" \
  --wait \
  --timeout 5m

# The local tag is reused, so restart to run the images just loaded into kind.
"${KUBECTL[@]}" rollout restart \
  deployment/semantic-operator-manager deployment/semantic-operator-server
"${KUBECTL[@]}" rollout status deployment/semantic-operator-manager --timeout=2m
"${KUBECTL[@]}" rollout status deployment/semantic-operator-server --timeout=2m

echo "Semantic operator is ready in namespace $NAMESPACE"
