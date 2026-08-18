#!/usr/bin/env bash
# Deploy all three engine identity modes as parallel operator releases, one per
# namespace (sem-static, sem-passthrough, sem-exchange), all sharing the Trino
# engine and Keycloak in the infra namespace. This is the fixture the per-PR Go
# e2e asserts against: three stable endpoints, one per mode, with no mode
# switching mid-test.
set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-semantic-operator-dev}"
KUBECONFIG_PATH="${KIND_KUBECONFIG:-$ROOT_DIR/.kube/config}"
INFRA_NAMESPACE="${KIND_NAMESPACE:-semantic-system}"
RELEASE_NAME="${KIND_RELEASE_NAME:-semantic-operator}"
IMAGE_BASE="${KIND_IMAGE_BASE:-semantic-operator-kind}"
IMAGE_TAG="${KIND_IMAGE_TAG:-local}"
PLATFORM="${KIND_PLATFORM:-linux/amd64}"
MANAGER_PW="${TRINO_MANAGER_PASSWORD:-manager}"
SERVER_PW="${TRINO_SERVER_PASSWORD:-server}"
VALUES_DIR="$ROOT_DIR/test/e2e/helm-values"
MODEL="$ROOT_DIR/test/e2e/auth/models/tpch-orders.yaml"

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME")
HELM=(helm --kubeconfig "$KUBECONFIG_PATH" --kube-context "kind-$CLUSTER_NAME")

# Build and load the images once for all three releases.
make -C "$ROOT_DIR" docker-build \
  IMAGE_BASE="$IMAGE_BASE" TAG="$IMAGE_TAG" PLATFORM="$PLATFORM"
kind load docker-image "$IMAGE_BASE/manager:$IMAGE_TAG" "$IMAGE_BASE/server:$IMAGE_TAG" \
  --name "$CLUSTER_NAME"

"${KUBECTL[@]}" -n "$INFRA_NAMESPACE" rollout status deployment/trino --timeout=5m

for mode in static passthrough exchange; do
  ns="sem-$mode"
  "${KUBECTL[@]}" create namespace "$ns" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -

  # Per-namespace engine credentials matching the shared Trino password file.
  "${KUBECTL[@]}" -n "$ns" create secret generic engine-manager-cred \
    --from-literal=password="$MANAGER_PW" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
  "${KUBECTL[@]}" -n "$ns" create secret generic engine-server-cred \
    --from-literal=password="$SERVER_PW" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -

  values=(--values "$VALUES_DIR/auth.yaml" --values "$VALUES_DIR/$mode.yaml")
  if [[ "$mode" == exchange ]]; then
    "${KUBECTL[@]}" -n "$ns" create secret generic engine-exchange-cred \
      --from-literal=client-secret=semantic-server-secret --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
  fi

  "${HELM[@]}" upgrade --install "$RELEASE_NAME" "$ROOT_DIR/charts/semantic-operator" \
    --namespace "$ns" \
    --create-namespace \
    "${values[@]}" \
    --set-string "image.manager.repository=$IMAGE_BASE/manager" \
    --set-string "image.server.repository=$IMAGE_BASE/server" \
    --set-string "image.tag=$IMAGE_TAG" \
    --wait \
    --timeout 5m

  "${KUBECTL[@]}" -n "$ns" rollout restart \
    deployment/"$RELEASE_NAME"-manager deployment/"$RELEASE_NAME"-server
  "${KUBECTL[@]}" -n "$ns" rollout status deployment/"$RELEASE_NAME"-server --timeout=2m

  # The model manifest pins the infra namespace; drop it and apply into $ns.
  sed '/^  namespace: semantic-system$/d' "$MODEL" | "${KUBECTL[@]}" -n "$ns" apply -f -
  "${KUBECTL[@]}" -n "$ns" wait --for=condition=Published semanticmodel/tpch-orders --timeout=2m

  echo "  $mode -> service/$RELEASE_NAME-server.$ns.svc:8090"
done

echo "identity-mode releases ready: sem-static, sem-passthrough, sem-exchange"
