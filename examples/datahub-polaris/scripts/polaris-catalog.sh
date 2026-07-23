#!/usr/bin/env bash
# Create the Iceberg REST catalog 'chd' on Garage. Idempotent.
# Uses the exact token + management-API calls validated offline against the
# real images (S3 storageConfigInfo: endpoint + pathStyleAccess + stsUnavailable;
# server holds the Garage key in AWS_* env). Runs curl inside the polaris pod.
. "$(dirname "$0")/lib.sh"

NS=chd
CATALOG="${POLARIS_CATALOG:-chd}"
BASE="s3://iceberg-warehouse/${CATALOG}"
ENDPOINT="http://garage.chd.svc.cluster.local:3900"

SECRET="$(kubectl -n "$NS" get secret polaris-credentials -o jsonpath='{.data.ROOT_CLIENT_SECRET}' | base64 -d)"
REGION="$(kubectl -n "$NS" get secret garage-credentials -o jsonpath='{.data.region}' | base64 -d)"
[ -n "$SECRET" ] || die "polaris root secret not found (is polaris-credentials created?)"
[ -n "$REGION" ] || die "garage region not found (run garage-up first)"

log "creating catalog '$CATALOG' (base $BASE) on Garage via Polaris management API"
kubectl -n "$NS" exec -i deploy/polaris -- bash -s -- "$SECRET" "$CATALOG" "$BASE" "$ENDPOINT" "$REGION" <<'POD'
set -eu
SECRET="$1"; CATALOG="$2"; BASE="$3"; ENDPOINT="$4"; REGION="$5"
API=http://localhost:8181
ACCESS=$(curl -s -X POST "$API/api/catalog/v1/oauth/tokens" -H 'Polaris-Realm: POLARIS' \
  -d grant_type=client_credentials -d client_id=root -d "client_secret=$SECRET" -d scope=PRINCIPAL_ROLE:ALL \
  | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
[ -n "$ACCESS" ] || { echo "TOKEN_FAIL"; exit 1; }
code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $ACCESS" -H 'Polaris-Realm: POLARIS' \
  "$API/api/management/v1/catalogs/$CATALOG")
if [ "$code" = 200 ]; then echo "CATALOG_EXISTS"; exit 0; fi
BODY=$(printf '{"catalog":{"type":"INTERNAL","name":"%s","properties":{"default-base-location":"%s"},"storageConfigInfo":{"storageType":"S3","allowedLocations":["%s"],"endpoint":"%s","pathStyleAccess":true,"stsUnavailable":true,"region":"%s"}}}' \
  "$CATALOG" "$BASE" "$BASE" "$ENDPOINT" "$REGION")
code=$(curl -s -o /tmp/cr -w '%{http_code}' -X POST "$API/api/management/v1/catalogs" \
  -H "Authorization: Bearer $ACCESS" -H 'Polaris-Realm: POLARIS' -H 'Content-Type: application/json' -d "$BODY")
echo "CREATE_HTTP=$code"; head -c 500 /tmp/cr; echo
[ "$code" = 201 ] || [ "$code" = 200 ]
POD
rc=$?
[ "$rc" = 0 ] || die "catalog creation failed (see output above)"
log "catalog '$CATALOG' ready"
