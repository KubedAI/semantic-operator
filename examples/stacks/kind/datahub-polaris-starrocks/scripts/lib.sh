#!/usr/bin/env bash
# Shared environment for the local demo scripts. Source this; do not execute.
set -euo pipefail

# Resolve local/ root regardless of caller CWD.
LOCAL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export LOCAL_DIR
# Prefer tools fetched by `make tools` into ./bin, then fall back to PATH.
export PATH="$LOCAL_DIR/bin:$PATH"
DATA_DIR="$LOCAL_DIR/data"
DEPLOY_DIR="$LOCAL_DIR/deploy"
export DATA_DIR DEPLOY_DIR

# Repo root, resolved via git for consistency (rather than fragile ../.. walks).
# Used to build the operator images from the repo's chart + cmd trees.
ROOT_DIR="$(git -C "$LOCAL_DIR" rev-parse --show-toplevel)"
export ROOT_DIR

# Pinned kind cluster + local kubeconfig. We NEVER write ~/.kube/config; every
# kubectl/helm/kind call in this project uses this local kubeconfig. kind itself
# must be on PATH.
CLUSTER_NAME="${CLUSTER_NAME:-account-demo-local}"
export KUBECONFIG="${KUBECONFIG:-$LOCAL_DIR/.kube/config}"
export CLUSTER_NAME
mkdir -p "$(dirname "$KUBECONFIG")"

# Load pinned versions (KEY=VALUE, comment-safe).
set -a; . "$DEPLOY_DIR/versions.lock"; set +a

log()  { printf '\033[1;34m[%s]\033[0m %s\n' "$(date +%H:%M:%S)" "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[fail]\033[0m %s\n' "$*" >&2; exit 1; }

# Read image refs from deploy/images/images.txt (skip comments/blanks).
image_refs() { grep -vE '^\s*(#|$)' "$DEPLOY_DIR/images/images.txt"; }
