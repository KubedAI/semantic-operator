#!/usr/bin/env bash
# Create local TLS, deploy Caddy, and install the in-cluster issuer host mapping.
. "$(dirname "$0")/lib.sh"

for command in openssl kubectl awk sed cmp cp uv; do
  command -v "$command" >/dev/null 2>&1 || die "required command not found: $command"
done

TLS_DIR="$LOCAL_DIR/.tmp/tls"
CUSTOM_CERT="${GATEWAY_TLS_CERT:-}"
CUSTOM_KEY="${GATEWAY_TLS_KEY:-}"
CUSTOM_CA="${GATEWAY_CA_CERT:-}"
custom_count=0
[ -n "$CUSTOM_CERT" ] && custom_count=$((custom_count + 1))
[ -n "$CUSTOM_KEY" ] && custom_count=$((custom_count + 1))
[ -n "$CUSTOM_CA" ] && custom_count=$((custom_count + 1))
[ "$custom_count" -eq 0 ] || [ "$custom_count" -eq 3 ] || \
  die "set GATEWAY_TLS_CERT, GATEWAY_TLS_KEY, and GATEWAY_CA_CERT together"

umask 077
mkdir -p "$TLS_DIR"
chmod 700 "$TLS_DIR"

if [ "$custom_count" -eq 3 ]; then
  TLS_CERT="$CUSTOM_CERT"
  TLS_KEY="$CUSTOM_KEY"
  CA_CERT="$CUSTOM_CA"
else
  CA_KEY="$TLS_DIR/ca.key"
  CA_CERT="$TLS_DIR/ca.crt"
  TLS_KEY="$TLS_DIR/tls.key"
  TLS_CERT="$TLS_DIR/tls.crt"

  if [ -e "$CA_KEY" ] || [ -e "$CA_CERT" ]; then
    [ -s "$CA_KEY" ] && [ -s "$CA_CERT" ] || die "incomplete generated CA under $TLS_DIR; remove it and retry"
  else
    log "generating local CA under .tmp/tls"
    openssl req -x509 -newkey rsa:3072 -sha256 -nodes -days 3650 \
      -subj "/CN=saas-accounts local CA" -keyout "$CA_KEY" -out "$CA_CERT" >/dev/null 2>&1
  fi

  if [ ! -s "$TLS_KEY" ] || [ ! -s "$TLS_CERT" ]; then
    log "generating wildcard leaf certificate for *.localtest.me"
    rm -f "$TLS_KEY" "$TLS_CERT" "$TLS_DIR/tls.csr" "$TLS_DIR/leaf.ext"
    cat >"$TLS_DIR/leaf.ext" <<'EOF'
subjectAltName=DNS:*.localtest.me,DNS:localtest.me
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
basicConstraints=critical,CA:FALSE
EOF
    openssl req -new -newkey rsa:2048 -sha256 -nodes -subj "/CN=*.localtest.me" \
      -keyout "$TLS_KEY" -out "$TLS_DIR/tls.csr" >/dev/null 2>&1
    openssl x509 -req -sha256 -days 397 -in "$TLS_DIR/tls.csr" \
      -CA "$CA_CERT" -CAkey "$CA_KEY" -CAcreateserial \
      -extfile "$TLS_DIR/leaf.ext" -out "$TLS_CERT" >/dev/null 2>&1
    rm -f "$TLS_DIR/tls.csr" "$TLS_DIR/leaf.ext"
  fi
  chmod 600 "$CA_KEY" "$TLS_KEY"
  chmod 644 "$CA_CERT" "$TLS_CERT"
fi

for file in "$TLS_CERT" "$TLS_KEY" "$CA_CERT"; do
  [ -r "$file" ] || die "TLS file is not readable: $file"
done
openssl x509 -in "$TLS_CERT" -noout -checkend 0 >/dev/null || die "gateway leaf certificate is invalid or expired"
openssl x509 -in "$CA_CERT" -noout -checkend 0 >/dev/null || die "gateway CA certificate is invalid or expired"
openssl pkey -in "$TLS_KEY" -noout >/dev/null 2>&1 || die "gateway leaf private key is invalid"
cert_key="$(openssl x509 -in "$TLS_CERT" -pubkey -noout | openssl pkey -pubin -outform DER 2>/dev/null | openssl sha256)"
private_key="$(openssl pkey -in "$TLS_KEY" -pubout -outform DER 2>/dev/null | openssl sha256)"
[ "$cert_key" = "$private_key" ] || die "gateway leaf certificate and private key do not match"
openssl verify -CAfile "$CA_CERT" "$TLS_CERT" >/dev/null || die "gateway leaf certificate is not signed by the supplied CA"
for host in auth.localtest.me datahub.localtest.me chat.localtest.me; do
  openssl x509 -in "$TLS_CERT" -noout -checkhost "$host" >/dev/null || die "gateway certificate does not cover $host"
done

# Keep one stable public-only handoff path for downstream trust and SSH copy,
# including when custom certificate paths are supplied. Never copy the CA key.
TRUST_CA="$TLS_DIR/trust-ca.crt"
if ! cmp -s "$CA_CERT" "$TRUST_CA"; then
  cp "$CA_CERT" "$TRUST_CA"
  chmod 644 "$TRUST_CA"
fi

log "applying HTTPS gateway in namespace account-demo"
kubectl apply -f "$DEPLOY_DIR/namespaces.yaml"
kubectl -n account-demo create secret tls gateway-tls --cert="$TLS_CERT" --key="$TLS_KEY" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n account-demo create configmap gateway-ca --from-file=ca.crt="$TRUST_CA" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f "$DEPLOY_DIR/gateway/gateway.yaml"

gateway_hash="$(cat "$DEPLOY_DIR/gateway/gateway.yaml" "$TLS_CERT" "$TLS_KEY" | openssl dgst -sha256 | awk '{print $NF}')"
kubectl -n account-demo patch deployment https-gateway --type=merge \
  -p "{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"saas-accounts-demo.local/gateway-config-sha256\":\"$gateway_hash\"}}}}}"

gateway_ip="$(kubectl -n account-demo get service https-gateway -o jsonpath='{.spec.clusterIP}')"
[[ "$gateway_ip" =~ ^[0-9a-fA-F:.]+$ ]] || die "invalid gateway ClusterIP: ${gateway_ip:-<empty>}"
coredns_current="$TLS_DIR/coredns-current.json"
coredns_patch="$TLS_DIR/coredns-patch.json"
coredns_rollback="$TLS_DIR/coredns-rollback.json"
coredns_status="$TLS_DIR/coredns-status"
kubectl -n kube-system get configmap coredns -o json >"$coredns_current"

uv run "$LOCAL_DIR/scripts/coredns_transform.py" "$gateway_ip" "$coredns_current" "$coredns_patch" "$coredns_rollback" "$coredns_status"

coredns_mode="$(sed -n '1p' "$coredns_status")"
coredns_change="$(sed -n '2p' "$coredns_status")"
if [ "$coredns_change" = changed ]; then
  log "updating split DNS through CoreDNS $coredns_mode"
  kubectl -n kube-system patch configmap coredns --type=merge \
    --patch-file="$coredns_patch"
  kubectl -n kube-system rollout restart deployment/coredns
  if ! kubectl -n kube-system rollout status deployment/coredns --timeout=180s; then
    warn "CoreDNS rollout failed; restoring the previous ConfigMap data"
    kubectl -n kube-system patch configmap coredns --type=merge \
      --patch-file="$coredns_rollback" || true
    kubectl -n kube-system rollout restart deployment/coredns || true
    kubectl -n kube-system rollout status deployment/coredns --timeout=180s || true
    die "CoreDNS rejected the split-DNS configuration"
  fi
elif [ "$coredns_change" = unchanged ]; then
  log "CoreDNS split DNS is already current ($coredns_mode)"
else
  die "unexpected CoreDNS transformer status: ${coredns_change:-<empty>}"
fi

log "waiting for HTTPS gateway"
kubectl -n account-demo rollout status deployment/https-gateway --timeout=180s
log "gateway ready at https://{datahub,auth,chat}.localtest.me:8443"
log "trust or copy the public CA at $TRUST_CA"
