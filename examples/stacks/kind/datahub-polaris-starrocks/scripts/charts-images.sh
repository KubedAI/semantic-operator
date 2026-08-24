#!/usr/bin/env bash
# Enumerate the exact image refs the vendored DataHub + prerequisites charts
# render with our local values, and rewrite the block below the marker in
# images.txt. Offline (helm template only) and re-runnable: it always regenerates
# the block, so it stays in sync with the pinned charts and values. Images for
# raw manifests are listed by hand above the marker and are left untouched.
. "$(dirname "$0")/lib.sh"
export HELM_CACHE_HOME="${HELM_CACHE_HOME:-/tmp/helm/cache}"
export HELM_CONFIG_HOME="${HELM_CONFIG_HOME:-/tmp/helm/config}"
export HELM_DATA_HOME="${HELM_DATA_HOME:-/tmp/helm/data}"

CHARTS="$DEPLOY_DIR/datahub/charts"
DHV="$DEPLOY_DIR/datahub"
IMAGES="$DEPLOY_DIR/images/images.txt"
MARKER='# --- Appended by `make charts-images`'

PREREQ_TGZ="$CHARTS/datahub-prerequisites-${DATAHUB_PREREQ_CHART_VERSION}.tgz"
DH_TGZ="$CHARTS/datahub-${DATAHUB_HELM_CHART_VERSION}.tgz"
[ -f "$PREREQ_TGZ" ] || die "vendored chart missing: $PREREQ_TGZ (run 'make charts-vendor')"
[ -f "$DH_TGZ" ] || die "vendored chart missing: $DH_TGZ (run 'make charts-vendor')"

tmp="$(mktemp)"
helm template prerequisites "$PREREQ_TGZ" -f "$DHV/prereqs-values.yaml" >> "$tmp"
helm template datahub        "$DH_TGZ"     -f "$DHV/values.yaml"        >> "$tmp"
refs="$(grep -hoE 'image: *"?[^"[:space:]]+"?' "$tmp" | sed -E 's/image: *//; s/"//g' | sort -u)"
rm -f "$tmp"
[ -n "$refs" ] || die "no images enumerated — chart render produced nothing"

# Preserve everything through the marker line, then regenerate the block.
sed -n "1,/$(printf '%s' "$MARKER" | sed 's/[.[\*^$]/\\&/g')/p" "$IMAGES" > "$IMAGES.new"
{
  echo "# DataHub ${DATAHUB_HELM_CHART_VERSION} + prerequisites"
  echo "# ${DATAHUB_PREREQ_CHART_VERSION} — enumerated by helm template on the vendored"
  echo "# charts with local values. Do not hand-edit; re-run 'make charts-images'."
  echo "$refs"
} >> "$IMAGES.new"
mv "$IMAGES.new" "$IMAGES"

log "images.txt updated with $(echo "$refs" | wc -l | tr -d ' ') DataHub/prereq image refs:"
echo "$refs" | sed 's/^/  /'
