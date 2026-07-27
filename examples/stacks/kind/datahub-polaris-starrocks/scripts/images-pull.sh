#!/usr/bin/env bash
# One-time networked fetch: pull each pinned image into the local Docker cache.
# 'make images-load' then loads them into the kind node with 'kind load'.
# Resumable: an image already in the Docker cache is skipped (FORCE=1 to re-pull).
. "$(dirname "$0")/lib.sh"

FORCE="${FORCE:-0}"
refs=()
while IFS= read -r _ref; do refs+=("$_ref"); done < <(image_refs)
[ "${#refs[@]}" -gt 0 ] || die "no image refs in $DEPLOY_DIR/images/images.txt"

log "pulling ${#refs[@]} images into the Docker cache"
fail=0
for ref in "${refs[@]}"; do
  if [ "$FORCE" != "1" ] && docker image inspect "$ref" >/dev/null 2>&1; then
    log "cached: $ref"
    continue
  fi
  log "pull: $ref"
  if ! docker pull "$ref"; then
    warn "FAILED to pull $ref (check the ref in deploy/images/images.txt / versions.lock)"
    fail=$((fail+1))
  fi
done
[ "$fail" -eq 0 ] || die "$fail image(s) failed to pull; fix refs and re-run"
log "done"
