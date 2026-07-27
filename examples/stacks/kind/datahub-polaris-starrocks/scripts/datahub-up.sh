#!/usr/bin/env bash
# Install DataHub and its prerequisites (OpenSearch + Kafka)
# into the 'datahub' namespace, backed by the shared Postgres 'datahub' DB.
# Prereqs come up first (DataHub's system-update job needs OpenSearch + Kafka
# ready), then the DataHub chart, then the NodePorts. Everything is single
# replica / minimal resources (see deploy/datahub/*.yaml). Images must already
# be loaded (make charts-images images-pull images-load).
. "$(dirname "$0")/lib.sh"

CHARTS="$DEPLOY_DIR/datahub/charts"
DHV="$DEPLOY_DIR/datahub"
PREREQ_TGZ="$CHARTS/datahub-prerequisites-${DATAHUB_PREREQ_CHART_VERSION}.tgz"
DH_TGZ="$CHARTS/datahub-${DATAHUB_HELM_CHART_VERSION}.tgz"
[ -f "$PREREQ_TGZ" ] && [ -f "$DH_TGZ" ] || die "vendored charts missing; run 'make charts-vendor'"

log "namespace datahub"
kubectl apply -f "$DEPLOY_DIR/namespaces.yaml"

log "note: this installs OpenSearch, Kafka, and DataHub with --wait; expect 5-10 minutes"

log "mirroring the datahub Postgres password into ns datahub (Secret datahub-postgres)"
PW="$(kubectl -n chd get secret postgres-credentials -o jsonpath='{.data.DATAHUB_DB_PASSWORD}' | base64 -d)"
[ -n "$PW" ] || die "could not read DATAHUB_DB_PASSWORD from chd/postgres-credentials (is Postgres deployed?)"
kubectl -n datahub create secret generic datahub-postgres \
  --from-literal=postgres-password="$PW" \
  --dry-run=client -o yaml | kubectl apply -f -

log "installing prerequisites (OpenSearch) — release name MUST be 'prerequisites'"
helm upgrade --install prerequisites "$PREREQ_TGZ" \
  --namespace datahub -f "$DHV/prereqs-values.yaml" --wait --timeout 12m

log "deploying single-node Apache Kafka (KRaft) as prerequisites-kafka"
kubectl apply -f "$DHV/kafka.yaml"
kubectl -n datahub rollout status statefulset/prerequisites-kafka --timeout=300s

log "installing DataHub (GMS + frontend); the system-update job + rollout take several minutes — please wait"
helm upgrade --install datahub "$DH_TGZ" \
  --namespace datahub -f "$DHV/values.yaml" --wait --timeout 20m

log "exposing GMS (localhost:8080) and frontend (localhost:9002) on node ports"
kubectl apply -f "$DHV/nodeports.yaml"

kubectl -n datahub rollout status deploy/datahub-datahub-gms --timeout=300s
kubectl -n datahub rollout status deploy/datahub-datahub-frontend --timeout=300s
log "pods:"; kubectl -n datahub get pods
log "DataHub ready — GMS http://localhost:8080  UI http://localhost:9002"
log "next: datahub-ingest (Iceberg REST -> GMS), then datahub-enrich"
