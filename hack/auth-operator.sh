#!/usr/bin/env bash
# Deploy the operator against the shared TLS Trino in an engine identity mode
# and publish the tpch-orders identity model. AUTH_IDENTITY_MODE selects the
# mode: passthrough (default), static, or exchange. Each mode is a values
# overlay on auth.yaml, so engine configuration stays in files.
set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-semantic-operator-dev}"
KUBECONFIG_PATH="${KIND_KUBECONFIG:-$ROOT_DIR/.kube/config}"
NAMESPACE="${KIND_NAMESPACE:-semantic-system}"
RELEASE_NAME="${KIND_RELEASE_NAME:-semantic-operator}"
IMAGE_BASE="${KIND_IMAGE_BASE:-semantic-operator-kind}"
IMAGE_TAG="${KIND_IMAGE_TAG:-local}"
PLATFORM="${KIND_PLATFORM:-linux/amd64}"
MODE="${AUTH_IDENTITY_MODE:-static}"
VALUES_DIR="$ROOT_DIR/test/e2e/helm-values"
MODEL="$ROOT_DIR/test/e2e/auth/models/tpch-orders.yaml"

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME" --namespace "$NAMESPACE")
HELM=(helm --kubeconfig "$KUBECONFIG_PATH" --kube-context "kind-$CLUSTER_NAME")

values=(--values "$VALUES_DIR/auth.yaml")
case "$MODE" in
  static) values+=(--values "$VALUES_DIR/static.yaml") ;;
  passthrough) values+=(--values "$VALUES_DIR/passthrough.yaml") ;;
  exchange)
    values+=(--values "$VALUES_DIR/exchange.yaml")
    # Confidential client secret the server uses to exchange caller tokens.
    "${KUBECTL[@]}" create secret generic engine-exchange-cred \
      --from-literal=client-secret=semantic-server-secret \
      --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
    ;;
  *)
    echo "unknown AUTH_IDENTITY_MODE $MODE (use passthrough, static, or exchange)" >&2
    exit 2
    ;;
esac

"${KUBECTL[@]}" rollout status deployment/trino --timeout=5m

make -C "$ROOT_DIR" docker-build \
  IMAGE_BASE="$IMAGE_BASE" TAG="$IMAGE_TAG" PLATFORM="$PLATFORM"
kind load docker-image "$IMAGE_BASE/manager:$IMAGE_TAG" "$IMAGE_BASE/server:$IMAGE_TAG" \
  --name "$CLUSTER_NAME"

"${HELM[@]}" upgrade --install "$RELEASE_NAME" "$ROOT_DIR/charts/semantic-operator" \
  --namespace "$NAMESPACE" \
  --create-namespace \
  "${values[@]}" \
  --set-string "image.manager.repository=$IMAGE_BASE/manager" \
  --set-string "image.server.repository=$IMAGE_BASE/server" \
  --set-string "image.tag=$IMAGE_TAG" \
  --wait \
  --timeout 5m

# The local tag is reused, so restart to run the images just loaded into kind.
"${KUBECTL[@]}" rollout restart \
  deployment/semantic-operator-manager deployment/semantic-operator-server
"${KUBECTL[@]}" rollout status deployment/semantic-operator-server --timeout=2m

"${KUBECTL[@]}" apply -f "$MODEL"
"${KUBECTL[@]}" wait --for=condition=Published semanticmodel/tpch-orders --timeout=2m

echo "operator ready in $MODE mode; tpch-orders published"
