#!/usr/bin/env bash
# Load the pinned images from the local Docker cache into the kind node with one
# deduplicated archive import. Run after the cluster exists and after
# 'make images-pull' has populated the Docker cache.
. "$(dirname "$0")/lib.sh"

kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME" || die "cluster '$CLUSTER_NAME' not found; run 'make cluster-up' first"

refs=()
while IFS= read -r _ref; do refs+=("$_ref"); done < <(image_refs)
[ "${#refs[@]}" -gt 0 ] || die "no image refs in $DEPLOY_DIR/images/images.txt"

# Docker Desktop's containerd image store keeps the full multi-arch index in
# the local cache but only the blobs for the host platform. `kind load
# docker-image` shells out to `ctr images import --all-platforms`, which then
# fails with "content digest ...: not found" on the other platform's manifest.
# Exporting one single-platform archive works on both the classic and the
# containerd image store, deduplicates shared layers, and pays kind/containerd
# import setup only once. Older Docker without `docker save --platform` falls
# back to one batched `kind load docker-image` invocation.
PLATFORM="linux/$(docker version --format '{{.Server.Arch}}')"
SAVE_PLATFORM=0
if docker save --help 2>&1 | grep -q -- '--platform'; then
  SAVE_PLATFORM=1
fi

# Validate every ref before loading anything. A digest-qualified ref is exported
# through its tag alias so the archive carries a stable repository name instead
# of a synthetic import-<date> reference. The alias must resolve to the pinned
# image, so batching cannot silently weaken a digest pin.
export_refs=()
invalid=0
for ref in "${refs[@]}"; do
  if [ "$SAVE_PLATFORM" = 1 ] && [[ "$ref" == *@sha256:* ]]; then
    tag_ref="${ref%@*}"
    if ! pinned_id="$(docker image inspect --format '{{.Id}}' "$ref" 2>/dev/null)"; then
      warn "not in Docker cache: $ref"
      invalid=$((invalid + 1))
      continue
    fi
    if ! tagged_id="$(docker image inspect --format '{{.Id}}' "$tag_ref" 2>/dev/null)"; then
      warn "tag alias required for archive export is not in Docker cache: $tag_ref"
      invalid=$((invalid + 1))
      continue
    fi
    if [ "$tagged_id" != "$pinned_id" ]; then
      warn "tag alias does not resolve to pinned image: $tag_ref"
      invalid=$((invalid + 1))
      continue
    fi
    export_refs+=("$tag_ref")
  elif docker image inspect "$ref" >/dev/null 2>&1; then
    export_refs+=("$ref")
  else
    warn "not in Docker cache: $ref"
    invalid=$((invalid + 1))
  fi
done
[ "$invalid" -eq 0 ] || die "$invalid image(s) missing or unsafe to export; run 'make images-pull' and retry"

log "loading ${#export_refs[@]} images into kind node '$CLUSTER_NAME' (platform $PLATFORM, one batch)"
if [ "$SAVE_PLATFORM" = 1 ]; then
  docker save --platform "$PLATFORM" "${export_refs[@]}" \
    | kind load image-archive /dev/stdin --name "$CLUSTER_NAME"
else
  warn "docker save --platform is unavailable; using one batched kind load instead"
  kind load docker-image "${refs[@]}" --name "$CLUSTER_NAME"
fi
log "done"
