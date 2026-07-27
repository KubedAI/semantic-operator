#!/usr/bin/env bash
# Stage 8b: add the business metadata a data steward would own in DataHub —
# descriptions, glossary terms (business vocabulary), and a PII tag.
#
# In a real deployment this is done by people in the DataHub UI over weeks.
# Here it is scripted so the demo is reproducible. Enrichment then imports it
# into the semantic model: descriptions and terms become documentation and
# ai_context synonyms, the PII tag becomes governance.denyFields.
#
# Talks to GMS over a local port-forward (no public endpoint), authenticating
# with the in-cluster system credential.
set -euo pipefail
NS=datahub
PORT="${GMS_PORT:-8091}"
PLATFORM="${DATAHUB_PLATFORM:-trino}"
DATASET_PREFIX="${DATAHUB_DATASET_PREFIX:-polaris}"
SCHEMA="${POLARIS_SCHEMA:-osi_demo}"
ENV_FABRIC="${DATAHUB_ENV:-PROD}"

log() { printf '\033[1;32m[datahub-annotate]\033[0m %s\n' "$*"; }

SECRET=$(kubectl -n "$NS" get secret datahub-auth-secrets -o jsonpath='{.data.system_client_secret}' | base64 -d)
AUTH="Basic __datahub_system:${SECRET}"
GQL="http://localhost:${PORT}/api/graphql"

curl -sf -o /dev/null "http://localhost:${PORT}/health" ||
  { echo "GMS not reachable on localhost:${PORT}. Run: kubectl -n $NS port-forward svc/datahub-datahub-gms ${PORT}:8080" >&2; exit 1; }

gql() { # $1 = query, $2 = variables json
  curl -s -X POST "$GQL" -H 'Content-Type: application/json' -H "Authorization: $AUTH" \
    -d "$(python3 -c 'import json,sys; print(json.dumps({"query":sys.argv[1],"variables":json.loads(sys.argv[2])}))' "$1" "$2")"
}

urn() { printf 'urn:li:dataset:(urn:li:dataPlatform:%s,%s.%s.%s,%s)' "$PLATFORM" "$DATASET_PREFIX" "$SCHEMA" "$1" "$ENV_FABRIC"; }
field_urn() { printf '%s' "$(urn "$1")"; }

# --- glossary terms: the business vocabulary an agent grounds words onto ----
log "creating glossary terms"
for term in Revenue Buyer Merchandise Storefront; do
  gql 'mutation($in: CreateGlossaryEntityInput!) { createGlossaryTerm(input: $in) }' \
    "{\"in\":{\"id\":\"$term\",\"name\":\"$term\",\"description\":\"$term (demo business glossary)\"}}" >/dev/null
done

# --- PII tag -----------------------------------------------------------------
log "creating PII tag"
gql 'mutation($in: CreateTagInput!) { createTag(input: $in) }' \
  '{"in":{"id":"PII","name":"PII","description":"Personally identifiable information"}}' >/dev/null

# --- dataset documentation ---------------------------------------------------
log "documenting datasets"
describe() { # $1 = table, $2 = description
  gql 'mutation($in: DescriptionUpdateInput!) { updateDescription(input: $in) }' \
    "$(python3 -c 'import json,sys; print(json.dumps({"in":{"resourceUrn":sys.argv[1],"description":sys.argv[2]}}))' "$(urn "$1")" "$2")" >/dev/null
}
describe store_sales "Store sales fact table. One row per item per ticket."
describe customer    "Customer dimension. One row per customer."
describe item        "Item dimension: product catalog with brand and category."
describe store       "Store dimension, including headcount per store."
describe date_dim    "Date dimension with calendar attributes."

# --- column documentation + classification -----------------------------------
log "documenting and classifying columns"
describe_field() { # $1 = table, $2 = column, $3 = description
  gql 'mutation($in: DescriptionUpdateInput!) { updateDescription(input: $in) }' \
    "$(python3 -c 'import json,sys; print(json.dumps({"in":{"resourceUrn":sys.argv[1],"subResource":sys.argv[2],"subResourceType":"DATASET_FIELD","description":sys.argv[3]}}))' "$(urn "$1")" "$2" "$3")" >/dev/null
}
describe_field store_sales ss_ext_sales_price "Extended sales price for the line item, net of discounts."
describe_field store_sales ss_net_profit      "Net profit for the line item."
describe_field store       s_number_employees "Headcount at the store. Do not sum across a sales join."
describe_field customer    c_email_address    "Customer contact email."
describe_field item        i_category         "Merchandise category."

tag_field() { # $1 = table, $2 = column, $3 = tag id
  gql 'mutation($in: TagAssociationInput!) { addTag(input: $in) }' \
    "$(python3 -c 'import json,sys; print(json.dumps({"in":{"tagUrn":"urn:li:tag:"+sys.argv[3],"resourceUrn":sys.argv[1],"subResource":sys.argv[2],"subResourceType":"DATASET_FIELD"}}))' "$(urn "$1")" "$2" "$3")" >/dev/null
}
log "tagging PII"
tag_field customer c_email_address PII

term_field() { # $1 = table, $2 = column, $3 = term id
  gql 'mutation($in: TermAssociationInput!) { addTerm(input: $in) }' \
    "$(python3 -c 'import json,sys; print(json.dumps({"in":{"termUrn":"urn:li:glossaryTerm:"+sys.argv[3],"resourceUrn":sys.argv[1],"subResource":sys.argv[2],"subResourceType":"DATASET_FIELD"}}))' "$(urn "$1")" "$2" "$3")" >/dev/null
}
log "attaching glossary terms"
term_field store_sales ss_ext_sales_price Revenue
term_field item        i_category         Merchandise
term_field store       s_state            Storefront

term_dataset() { # $1 = table, $2 = term id
  gql 'mutation($in: TermAssociationInput!) { addTerm(input: $in) }' \
    "$(python3 -c 'import json,sys; print(json.dumps({"in":{"termUrn":"urn:li:glossaryTerm:"+sys.argv[2],"resourceUrn":sys.argv[1]}}))' "$(urn "$1")" "$2")" >/dev/null
}
term_dataset customer Buyer

log "annotated ${DATASET_PREFIX}.${SCHEMA} in DataHub"
