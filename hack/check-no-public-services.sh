#!/usr/bin/env bash
# Fail if anything that can be applied to a real cluster would publish a
# Service outside it.
#
# The semantic server authorizes callers from the X-Semantic-Role header and
# expects an authenticating proxy in front, so a LoadBalancer or NodePort
# means an unauthenticated query endpoint (on EKS, a public IP). Everything
# here is reached with `kubectl port-forward` instead.
#
# The kind stack under examples/stacks/kind/ is exempt: kind maps NodePorts to
# 127.0.0.1 on the developer's laptop, so they are not reachable off-host. Its
# manifests carry a warning header (enforced below) so nobody applies them to
# a real cluster by mistake.
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0

# 1. No LoadBalancer or NodePort in the chart or in cluster-bound examples.
targets=(charts)
[ -d examples/stacks/eks ] && targets+=(examples/stacks/eks)
if hits=$(grep -rnE '^\s*type:\s*(LoadBalancer|NodePort)' "${targets[@]}" 2>/dev/null); then
  echo "ERROR: Service type must be ClusterIP (use kubectl port-forward):"
  echo "$hits"
  fail=1
fi

# 2. The chart must not hand out an escape hatch by default.
if grep -qE '^\s*allowExternalExposure:\s*true' charts/semantic-operator/values.yaml; then
  echo "ERROR: charts/semantic-operator/values.yaml ships allowExternalExposure: true"
  fail=1
fi

# 3. Every kind manifest that declares a NodePort must warn that it is
#    laptop-only, so a stray `kubectl apply` against a real cluster is an
#    obvious mistake rather than a silent exposure.
if [ -d examples/stacks/kind ]; then
  while IFS= read -r f; do
    if ! grep -q 'LOCAL KIND ONLY' "$f"; then
      echo "ERROR: $f declares a NodePort but lacks the 'LOCAL KIND ONLY' warning header"
      fail=1
    fi
  done < <(grep -rlE '^\s*type:\s*NodePort' examples/stacks/kind 2>/dev/null || true)
fi

if [ "$fail" -eq 0 ]; then
  echo "no public Service exposure found"
fi
exit "$fail"
