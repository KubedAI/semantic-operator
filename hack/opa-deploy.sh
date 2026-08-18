#!/usr/bin/env bash
# Deploy OPA and load the retail policy.
set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-semantic-operator-dev}"
KUBECONFIG_PATH="${KIND_KUBECONFIG:-$ROOT_DIR/.kube/config}"
NAMESPACE="${KIND_NAMESPACE:-semantic-system}"
POLICY="$ROOT_DIR/test/e2e/opa/retail.rego"
RESOURCES="$ROOT_DIR/test/e2e/opa/resources.yaml"

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME" --namespace "$NAMESPACE")

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

echo "OPA and retail policy are ready"
