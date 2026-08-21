#!/usr/bin/env bash
# Fetch the pinned CLI tools (kind, kubectl, helm, uv) into ./bin for this
# example, matching the host OS and architecture. Supports Linux and macOS on
# amd64 and arm64. Idempotent: a tool already at the pinned version is skipped.
#
# Docker, make, git, curl, openssl, and the Go toolchain stay host
# prerequisites; they are not fetched here. The workload container images are
# amd64-only (see versions.lock); on Apple Silicon they run under Docker's
# emulation. The CLI binaries fetched here are always host-native.
#
# Versions come from deploy/versions.lock. Downloads are over HTTPS from each
# tool's official location. UV_LIBC=musl fetches the musl uv build on Linux
# (for example Alpine).
. "$(dirname "$0")/lib.sh"

BIN_DIR="$LOCAL_DIR/bin"
mkdir -p "$BIN_DIR"

case "$(uname -s)" in
  Linux)  OS=linux ;;
  Darwin) OS=darwin ;;
  *) die "unsupported OS $(uname -s); this installer supports Linux and macOS" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) die "unsupported architecture $(uname -m); supported: amd64, arm64" ;;
esac
log "host $OS/$ARCH; target $BIN_DIR"

fetch() { curl -fsSL -o "$2" "$1"; }

have_version() { # tool -> normalized version (no leading v), or empty
  [ -x "$BIN_DIR/$1" ] || { echo ""; return; }
  local v=""
  case "$1" in
    kind)    v="$("$BIN_DIR/kind" --version 2>/dev/null | awk '{print $NF}')" ;;
    kubectl) v="$("$BIN_DIR/kubectl" version --client 2>/dev/null | awk '/Client Version/{print $NF}')" ;;
    helm)    v="$("$BIN_DIR/helm" version --short 2>/dev/null | sed 's/+.*//')" ;;
    uv)      v="$("$BIN_DIR/uv" --version 2>/dev/null | awk '{print $2}')" ;;
  esac
  echo "${v#v}"
}
up_to_date() { [ "$(have_version "$1")" = "${2#v}" ]; }

install_kind() {
  up_to_date kind "$KIND_VERSION" && { log "kind $KIND_VERSION present"; return; }
  local url="https://github.com/kubernetes-sigs/kind/releases/download/${KIND_VERSION}/kind-${OS}-${ARCH}"
  local tmp; tmp="$(mktemp -d)"
  log "fetching kind ${KIND_VERSION}"
  fetch "$url" "$tmp/kind" || die "download failed: $url"
  install -m 0755 "$tmp/kind" "$BIN_DIR/kind"; rm -rf "$tmp"
  log "installed $BIN_DIR/kind"
}
install_kubectl() {
  up_to_date kubectl "$KUBECTL_VERSION" && { log "kubectl $KUBECTL_VERSION present"; return; }
  local base="https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/${OS}/${ARCH}"
  local tmp; tmp="$(mktemp -d)"
  log "fetching kubectl ${KUBECTL_VERSION}"
  fetch "$base/kubectl" "$tmp/kubectl" || die "download failed: $base/kubectl"
  install -m 0755 "$tmp/kubectl" "$BIN_DIR/kubectl"; rm -rf "$tmp"
  log "installed $BIN_DIR/kubectl"
}
install_helm() {
  up_to_date helm "$HELM_VERSION" && { log "helm $HELM_VERSION present"; return; }
  local file="helm-${HELM_VERSION}-${OS}-${ARCH}.tar.gz"
  local url="https://get.helm.sh/${file}"
  local tmp; tmp="$(mktemp -d)"
  log "fetching helm ${HELM_VERSION}"
  fetch "$url" "$tmp/$file" || die "download failed: $url"
  tar -xzf "$tmp/$file" -C "$tmp"
  install -m 0755 "$tmp/${OS}-${ARCH}/helm" "$BIN_DIR/helm"; rm -rf "$tmp"
  log "installed $BIN_DIR/helm"
}
install_uv() {
  up_to_date uv "$UV_VERSION" && { log "uv $UV_VERSION present"; return; }
  local libc="${UV_LIBC:-gnu}" triple
  case "$OS/$ARCH" in
    linux/amd64)  triple="x86_64-unknown-linux-${libc}" ;;
    linux/arm64)  triple="aarch64-unknown-linux-${libc}" ;;
    darwin/amd64) triple="x86_64-apple-darwin" ;;
    darwin/arm64) triple="aarch64-apple-darwin" ;;
  esac
  local file="uv-${triple}.tar.gz"
  local url="https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/${file}"
  local tmp; tmp="$(mktemp -d)"
  log "fetching uv ${UV_VERSION}"
  fetch "$url" "$tmp/$file" || die "download failed: $url"
  tar -xzf "$tmp/$file" -C "$tmp"
  install -m 0755 "$tmp/uv-${triple}/uv" "$BIN_DIR/uv"; rm -rf "$tmp"
  log "installed $BIN_DIR/uv"
}

install_kind
install_kubectl
install_helm
install_uv
log "tools ready in $BIN_DIR. The make targets use them automatically. To run kind, kubectl, helm, or uv directly, add $BIN_DIR to your PATH."
