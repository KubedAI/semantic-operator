#!/usr/bin/env bash
# Deploy Keycloak and import the semantic realm. The single Trino engine enables
# JWT authentication and fetches the realm JWKS when it starts, so Keycloak must
# be running before the engine. Keycloak runs in dev mode with an in-memory
# database, so a pod restart re-imports the realm from the ConfigMap.
set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-semantic-operator-dev}"
KUBECONFIG_PATH="${KIND_KUBECONFIG:-$ROOT_DIR/.kube/config}"
NAMESPACE="${KIND_NAMESPACE:-semantic-system}"
REALM="$ROOT_DIR/test/e2e/auth/keycloak/realm.json"
RESOURCES="$ROOT_DIR/test/e2e/auth/keycloak/resources.yaml"

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME" --namespace "$NAMESPACE")

"${KUBECTL[@]}" create namespace "$NAMESPACE" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
"${KUBECTL[@]}" create configmap keycloak-realm \
  --from-file=realm.json="$REALM" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
"${KUBECTL[@]}" apply -f "$RESOURCES"
"${KUBECTL[@]}" rollout status deployment/keycloak --timeout=5m

echo "Keycloak is ready at http://keycloak.$NAMESPACE.svc:8080"
