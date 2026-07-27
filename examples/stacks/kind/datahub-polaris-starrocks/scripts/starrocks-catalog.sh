#!/usr/bin/env bash
# Create the StarRocks Iceberg REST external catalog 'iceberg'
# pointing at Polaris (catalog/warehouse 'chd') with Garage as the S3 backend.
#
# Auth: OAuth2 client-credentials to Polaris (root principal). Polaris resolves
# the single POLARIS realm without a custom header (verified), so none is sent.
# Storage: static Garage key (Polaris does not vend credentials here), path-style.
. "$(dirname "$0")/lib.sh"

FE_POD="${FE_POD:-chd-fe-0}"

d() { kubectl -n chd get secret "$1" -o jsonpath="{.data.$2}" | base64 -d; }
GKEY="$(d garage-credentials AWS_ACCESS_KEY_ID)"
GSEC="$(d garage-credentials AWS_SECRET_ACCESS_KEY)"
GEP="$(d garage-credentials endpoint)"
GREG="$(d garage-credentials region)"
PID="$(d polaris-credentials ROOT_CLIENT_ID)"
PSEC="$(d polaris-credentials ROOT_CLIENT_SECRET)"
[ -n "$GKEY" ] && [ -n "$PSEC" ] || die "could not read garage/polaris secrets from namespace chd"

POLARIS_URI="http://polaris.chd.svc.cluster.local:8181/api/catalog"

read -r -d '' SQL <<EOF || true
CREATE EXTERNAL CATALOG IF NOT EXISTS iceberg PROPERTIES (
  "type" = "iceberg",
  "iceberg.catalog.type" = "rest",
  "iceberg.catalog.uri" = "${POLARIS_URI}",
  "iceberg.catalog.warehouse" = "chd",
  "iceberg.catalog.security" = "oauth2",
  "iceberg.catalog.oauth2.credential" = "${PID}:${PSEC}",
  "iceberg.catalog.oauth2.scope" = "PRINCIPAL_ROLE:ALL",
  "iceberg.catalog.oauth2.server-uri" = "${POLARIS_URI}/v1/oauth/tokens",
  "iceberg.catalog.vended-credentials-enabled" = "false",
  "aws.s3.endpoint" = "${GEP}",
  "aws.s3.enable_ssl" = "false",
  "aws.s3.enable_path_style_access" = "true",
  "aws.s3.region" = "${GREG}",
  "aws.s3.access_key" = "${GKEY}",
  "aws.s3.secret_key" = "${GSEC}"
);
SHOW CATALOGS;
EOF

# Drop first so re-runs pick up property changes (external catalog drop is
# metadata-only; it does not touch data in Garage or namespaces in Polaris).
log "dropping any existing 'iceberg' catalog (ignored if absent)"
kubectl -n chd exec -i "$FE_POD" -- mysql -h127.0.0.1 -P9030 -uroot \
  -e "DROP CATALOG iceberg;" 2>/dev/null || true

log "creating external catalog 'iceberg' -> Polaris ($POLARIS_URI, warehouse chd)"
kubectl -n chd exec -i "$FE_POD" -- mysql -h127.0.0.1 -P9030 -uroot -e "$SQL"

log "listing databases in the iceberg catalog:"
kubectl -n chd exec -i "$FE_POD" -- mysql -h127.0.0.1 -P9030 -uroot \
  -e "SET CATALOG iceberg; SHOW DATABASES;"
log "catalog 'iceberg' ready — next: data-load"
