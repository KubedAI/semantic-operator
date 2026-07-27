#!/usr/bin/env bash
# Stage 8a: ingest the Polaris tables into DataHub, through Trino.
#
# Runs DataHub's own ingestion image as an in-cluster Job, so the GMS
# credential never leaves the cluster and nothing needs a public endpoint.
# The trino source names datasets "<catalog>.<schema>.<table>", which is why
# enrichment later passes -datahub-dataset-prefix polaris.
set -euo pipefail
NS=datahub
NS_TRINO="${NS_TRINO:-trino}"
CATALOG="${TRINO_CATALOG:-polaris}"
SCHEMA="${POLARIS_SCHEMA:-osi_demo}"
JOB=datahub-ingest-polaris

log() { printf '\033[1;32m[datahub-ingest]\033[0m %s\n' "$*"; }

kubectl -n "$NS" delete job "$JOB" --ignore-not-found >/dev/null

kubectl -n "$NS" create configmap "$JOB-recipe" --dry-run=client -o yaml --from-literal=recipe.yml="
source:
  type: trino
  config:
    host_port: trino.${NS_TRINO}.svc.cluster.local:8080
    database: ${CATALOG}
    username: datahub
    schema_pattern:
      allow:
        - '^${SCHEMA}\$'
sink:
  type: datahub-rest
  config:
    server: http://datahub-datahub-gms.${NS}.svc.cluster.local:8080
    # DataHub's system authenticator takes 'Basic <clientId>:<clientSecret>'
    # verbatim (not base64). The REST sink's 'token' option would send a
    # Bearer personal access token instead, which we would have to mint and
    # rotate; the system credential is already in the cluster.
    extra_headers:
      Authorization: 'Basic __datahub_system:\${DATAHUB_GMS_TOKEN}'
" | kubectl apply -f - >/dev/null

# The system client secret is already in the cluster; mount it rather than
# copying it to the workstation.
kubectl -n "$NS" apply -f - >/dev/null <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: $JOB
spec:
  backoffLimit: 1
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: ingest
          image: acryldata/datahub-ingestion:v1.6.0
          command: ["/bin/sh", "-c"]
          args:
            - |
              pip install --quiet 'acryl-datahub[trino]' 2>/dev/null || true
              datahub ingest -c /recipe/recipe.yml
          env:
            - name: DATAHUB_GMS_TOKEN
              valueFrom:
                secretKeyRef: {name: datahub-auth-secrets, key: system_client_secret}
            - name: DATAHUB_SYSTEM_CLIENT_ID
              value: __datahub_system
          volumeMounts:
            - {name: recipe, mountPath: /recipe}
      volumes:
        - name: recipe
          configMap: {name: $JOB-recipe}
EOF

log "ingestion job started; waiting"
kubectl -n "$NS" wait --for=condition=complete "job/$JOB" --timeout=600s ||
  { kubectl -n "$NS" logs "job/$JOB" --tail=40; exit 1; }
kubectl -n "$NS" logs "job/$JOB" --tail=8
log "ingested ${CATALOG}.${SCHEMA} into DataHub"
