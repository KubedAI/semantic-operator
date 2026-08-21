#!/usr/bin/env bash
# Deploy the operator in one engine identity mode and publish the identity
# model. AUTH_IDENTITY_MODE selects the mode (static, passthrough, exchange) and
# KIND_ENGINE_TYPE selects the engine (trino or starrocks). The engine
# credentials are created in the target namespace, so this runs standalone for a
# single mode or once per namespace from the e2e orchestrator.
set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-semantic-operator-dev}"
KUBECONFIG_PATH="${KIND_KUBECONFIG:-$ROOT_DIR/.kube/config}"
NAMESPACE="${KIND_NAMESPACE:-semantic-system}"
# The engine and Keycloak live in the infra namespace; the operator release can
# live in its own namespace (the orchestrator uses sem-<mode>) and reaches the
# engine cross-namespace by the FQDN in the values file.
INFRA_NAMESPACE="${KIND_INFRA_NAMESPACE:-semantic-system}"
RELEASE_NAME="${KIND_RELEASE_NAME:-semantic-operator}"
IMAGE_BASE="${KIND_IMAGE_BASE:-semantic-operator-kind}"
IMAGE_TAG="${KIND_IMAGE_TAG:-local}"
PLATFORM="${KIND_PLATFORM:-linux/amd64}"
MODE="${AUTH_IDENTITY_MODE:-static}"
ENGINE_TYPE="${KIND_ENGINE_TYPE:-trino}"
MANAGER_PW="${ENGINE_MANAGER_PASSWORD:-manager}"
SERVER_PW="${ENGINE_SERVER_PASSWORD:-server}"
VALUES_DIR="$ROOT_DIR/test/e2e/helm-values"

case "$ENGINE_TYPE" in
trino)
  ENGINE_DEPLOY=trino
  BASE="$VALUES_DIR/auth.yaml"
  MODEL="$ROOT_DIR/test/e2e/auth/models/tpch-orders.yaml"
  MODEL_CR=tpch-orders
  ;;
starrocks)
  ENGINE_DEPLOY=starrocks
  BASE="$VALUES_DIR/auth-starrocks.yaml"
  MODEL="$ROOT_DIR/test/e2e/auth/models/retail-identity.yaml"
  MODEL_CR=retail-identity
  ;;
*)
  echo "unsupported KIND_ENGINE_TYPE=$ENGINE_TYPE (want trino or starrocks)" >&2
  exit 2
  ;;
esac

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME" --namespace "$NAMESPACE")
KUBECTL_INFRA=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME" --namespace "$INFRA_NAMESPACE")
HELM=(helm --kubeconfig "$KUBECONFIG_PATH" --kube-context "kind-$CLUSTER_NAME")

"${KUBECTL[@]}" create namespace "$NAMESPACE" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -

# Engine credentials in this namespace; their passwords match the engine's
# static users.
"${KUBECTL[@]}" create secret generic engine-manager-cred \
  --from-literal=password="$MANAGER_PW" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
"${KUBECTL[@]}" create secret generic engine-server-cred \
  --from-literal=password="$SERVER_PW" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -

values=(--values "$BASE")
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
    echo "unknown AUTH_IDENTITY_MODE $MODE (use static, passthrough, or exchange)" >&2
    exit 2
    ;;
esac

"${KUBECTL_INFRA[@]}" rollout status "deployment/$ENGINE_DEPLOY" --timeout=5m

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
  "deployment/$RELEASE_NAME-manager" "deployment/$RELEASE_NAME-server"
"${KUBECTL[@]}" rollout status "deployment/$RELEASE_NAME-server" --timeout=2m

# The model manifest pins the infra namespace; drop it and apply into $NAMESPACE.
sed '/^  namespace: semantic-system$/d' "$MODEL" | "${KUBECTL[@]}" apply -f -
"${KUBECTL[@]}" wait --for=condition=Published "semanticmodel/$MODEL_CR" --timeout=2m

echo "operator ready in $MODE mode ($ENGINE_TYPE); $MODEL_CR published in $NAMESPACE"
