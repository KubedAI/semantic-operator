#!/usr/bin/env bash
# Load the pinned images from the local Docker cache into the kind node with
# 'kind load'. Run after the cluster exists and after 'make images-pull' has
# populated the Docker cache.
. "$(dirname "$0")/lib.sh"

kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME" || die "cluster '$CLUSTER_NAME' not found; run 'make cluster-up' first"

mapfile -t refs < <(image_refs)
log "loading ${#refs[@]} images into kind node '$CLUSTER_NAME'"
missing=0
for ref in "${refs[@]}"; do
  if docker image inspect "$ref" >/dev/null 2>&1; then
    log "cache -> node: $ref"
    kind load docker-image "$ref" --name "$CLUSTER_NAME"
  else
    warn "not in Docker cache: $ref"
    missing=$((missing+1))
  fi
done
[ "$missing" -eq 0 ] || die "$missing image(s) missing from the Docker cache; run 'make images-pull'"
log "done"
