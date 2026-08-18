#!/usr/bin/env bash
# Deploy a single Trino coordinator that also executes worker tasks.
set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-semantic-operator-dev}"
KUBECONFIG_PATH="${KIND_KUBECONFIG:-$ROOT_DIR/.kube/config}"
NAMESPACE="${KIND_NAMESPACE:-semantic-system}"
RESOURCES="$ROOT_DIR/test/e2e/trino/resources.yaml"

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "kind-$CLUSTER_NAME")
KUBECTL_NS=("${KUBECTL[@]}" --namespace "$NAMESPACE")

"${KUBECTL[@]}" create namespace "$NAMESPACE" --dry-run=client -o yaml |
  "${KUBECTL[@]}" apply -f -

"${KUBECTL_NS[@]}" apply -f "$RESOURCES"
# The configuration uses subPath mounts, so restart after every apply.
"${KUBECTL_NS[@]}" rollout restart deployment/trino
"${KUBECTL_NS[@]}" rollout status deployment/trino --timeout=5m

for _ in {1..60}; do
  if "${KUBECTL_NS[@]}" exec deployment/trino -- trino --output-format TSV \
    --execute 'SELECT count(*) FROM memory.information_schema.schemata' >/dev/null 2>&1; then
    echo "Trino is ready at trino.$NAMESPACE.svc.cluster.local:8080"
    exit 0
  fi
  sleep 2
done

echo "Trino started, but the memory catalog is not ready" >&2
exit 1
