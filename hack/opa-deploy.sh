#!/usr/bin/env bash
# Deploy OPA, load the retail policy, and register the provider with the server.
set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-semantic-operator-dev}"
KUBECONFIG_PATH="${KIND_KUBECONFIG:-$ROOT_DIR/.kube/config}"
NAMESPACE="${KIND_NAMESPACE:-semantic-system}"
RELEASE_NAME="${KIND_RELEASE_NAME:-semantic-operator}"
POLICY="$ROOT_DIR/test/e2e/opa/retail.rego"
RESOURCES="$ROOT_DIR/test/e2e/opa/resources.yaml"

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME" --namespace "$NAMESPACE")
HELM=(helm --kubeconfig "$KUBECONFIG_PATH" --kube-context "kind-$CLUSTER_NAME")

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

# Keep the policy aligned when KIND_NAMESPACE is overridden.
sed "s/input.model.namespace == \"semantic-system\"/input.model.namespace == \"$NAMESPACE\"/" \
  "$POLICY" >"$tmp_dir/retail.rego"

"${KUBECTL[@]}" create configmap opa-retail-policy \
  --from-file="retail.rego=$tmp_dir/retail.rego" --dry-run=client -o yaml |
  "${KUBECTL[@]}" apply -f -
"${KUBECTL[@]}" apply -f "$RESOURCES"

# OPA reads file-backed policies at startup.
"${KUBECTL[@]}" rollout restart deployment/opa
"${KUBECTL[@]}" rollout status deployment/opa --timeout=2m

"${HELM[@]}" upgrade "$RELEASE_NAME" "$ROOT_DIR/charts/semantic-operator" \
  --namespace "$NAMESPACE" \
  --reuse-values \
  --set-string 'server.authorization.providers[0].name=retail-opa' \
  --set-string 'server.authorization.providers[0].type=opa' \
  --set-string "server.authorization.providers[0].url=http://opa.$NAMESPACE.svc.cluster.local:8181" \
  --set 'server.authorization.providers[0].timeoutSeconds=2' \
  --set 'server.authorization.providers[0].maxResponseBytes=1048576' \
  --set-string 'server.authorization.providers[0].opa.decisionPath=semantic/retail/decision' \
  --wait \
  --timeout 5m

echo "OPA provider retail-opa is ready"
