#!/usr/bin/env bash
# Single-node Garage — layout, buckets, access key, credentials.
# Idempotent-ish: safe to re-run; layout is only assigned once.
# (confirm-on-run): Garage CLI output wording (node id / key info) may vary by
# version — the parses below are the first thing to check if this misbehaves.
. "$(dirname "$0")/lib.sh"

BUCKETS=(iceberg-warehouse starrocks-storage)
G=(kubectl -n chd exec garage-0 -- /garage)

kubectl apply -f "$DEPLOY_DIR/namespaces.yaml"
kubectl apply -f "$DEPLOY_DIR/garage/garage.yaml"

log "waiting for garage statefulset"
kubectl -n chd rollout status statefulset/garage --timeout=180s

NODEID="$("${G[@]}" node id -q 2>/dev/null | cut -d@ -f1)"
[ -n "$NODEID" ] || die "could not read garage node id"
log "garage node: $NODEID"

if "${G[@]}" layout show 2>/dev/null | grep -q "$NODEID"; then
  log "layout already assigned; skipping"
else
  log "assigning single-node layout"
  "${G[@]}" layout assign -z dc1 -c 10G "$NODEID"
  ver="$("${G[@]}" layout show 2>/dev/null | awk '/Current cluster layout version/{print $NF}')"
  "${G[@]}" layout apply --version "$(( ${ver:-0} + 1 ))"
fi

for b in "${BUCKETS[@]}"; do
  "${G[@]}" bucket create "$b" 2>/dev/null || log "bucket $b exists"
done

# Fixed, obviously-fake local-dev access key (all "deadbeef" — not a real
# credential, so secret scanners leave it alone). Garage key NAMES are not
# unique, so 'key create chd-key' on a re-run makes a duplicate and
# 'key info chd-key' then fails with "2 matching keys"; importing a fixed ID
# is idempotent.
KEYID="GKdeadbeefdeadbeefdeadbeef"
SECRET="deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
if "${G[@]}" key info "$KEYID" >/dev/null 2>&1; then
  log "access key $KEYID exists"
else
  log "importing fixed access key $KEYID"
  "${G[@]}" key import --yes -n chd-key "$KEYID" "$SECRET"
fi

for b in "${BUCKETS[@]}"; do
  "${G[@]}" bucket allow --read --write --owner "$b" --key "$KEYID"
done

log "storing garage-credentials secret in ns chd"
kubectl -n chd create secret generic garage-credentials \
  --from-literal=AWS_ACCESS_KEY_ID="$KEYID" \
  --from-literal=AWS_SECRET_ACCESS_KEY="$SECRET" \
  --from-literal=endpoint="http://garage.chd.svc.cluster.local:3900" \
  --from-literal=region="garage" \
  --dry-run=client -o yaml | kubectl apply -f -

log "garage OK. buckets: ${BUCKETS[*]}; key id: $KEYID"
log "host check: AWS_ACCESS_KEY_ID=$KEYID AWS_SECRET_ACCESS_KEY=<secret> aws --endpoint-url http://localhost:3900 --region garage s3 ls"
