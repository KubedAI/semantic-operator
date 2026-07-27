#!/usr/bin/env bash
# One-time networked fetch: vendor the pinned helm charts into deploy/<c>/charts
# so installs are offline and reproducible. Chart tarballs are committed.
#
# Repo URLs are (confirm-on-vendor): they could not be verified from the
# authoring sandbox. If a repo add/pull fails, correct the URL here.
. "$(dirname "$0")/lib.sh"

DATAHUB_REPO="https://helm.datahubproject.io/"
STARROCKS_REPO="https://starrocks.github.io/starrocks-kubernetes-operator"

log "adding + updating helm repos"
helm repo add datahub  "$DATAHUB_REPO"  >/dev/null 2>&1 || true
helm repo add starrocks "$STARROCKS_REPO" >/dev/null 2>&1 || true
helm repo update datahub starrocks

log "available versions (pin the ones you want in deploy/versions.lock):"
echo "--- datahub/datahub ---";               helm search repo datahub/datahub --versions              | head -5 || true
echo "--- datahub/datahub-prerequisites ---"; helm search repo datahub/datahub-prerequisites --versions | head -5 || true
echo "--- starrocks/kube-starrocks ---";      helm search repo starrocks/kube-starrocks --versions      | head -5 || true

pull() { # repo/chart  version  destdir
  local chart="$1" version="$2" dest="$3"
  [ -n "$version" ] || { warn "no pinned version for $chart; pin it in versions.lock then re-run"; return 0; }
  mkdir -p "$dest"
  log "pull $chart@$version -> $dest"
  helm pull "$chart" --version "$version" -d "$dest" || die "failed to pull $chart@$version"
}

pull datahub/datahub               "${DATAHUB_HELM_CHART_VERSION:-}"    "$DEPLOY_DIR/datahub/charts"
pull datahub/datahub-prerequisites "${DATAHUB_PREREQ_CHART_VERSION:-}"  "$DEPLOY_DIR/datahub/charts"
pull starrocks/kube-starrocks      "${KUBE_STARROCKS_CHART_VERSION:-}"  "$DEPLOY_DIR/starrocks/charts"

log "vendored charts:"; find "$DEPLOY_DIR" -name '*.tgz' | sort
log "next: tell the coordinator the versions shown above so they get pinned, then run 'make images-pull'"
