# External authorization policy for the account demo.
#
# This is the FIRST governance gate. The Semantic Operator sends every query
# request here before it compiles the model to SQL. A deny here means the
# request never becomes SQL. The built-in field and row governance in each
# SemanticModel still applies afterward, to whatever this gate allows.
#
# OPA serves this at /v1/data/semantic/query/allow. The operator posts
# {"input": {...}} and reads {"result": <bool>}. An undefined result is a deny,
# and the operator fails closed if OPA is unreachable.
package semantic.query

import rego.v1

# Fail closed. Anything not explicitly allowed is denied.
default allow := false

# The account demo publishes exactly these models. Listing them once here gates
# every model centrally, instead of repeating the rule in each SemanticModel.
account_models := {"saas_revenue", "saas_adoption", "saas_support"}

# Revenue-retention is a finance view of the business. One rule covers every
# model and every caller, which a per-model allowMetrics list cannot do without
# being copied into each CR.
finance_only_metrics := {"gross_revenue_retention", "net_revenue_retention"}

# Normal analytics: any account model, as long as the request does not ask for a
# finance-restricted metric.
allow if {
	input.action == "query"
	input.model.name in account_models
	count(finance_only_requested) == 0
}

# Finance-restricted metrics: allowed only for a finance identity.
allow if {
	input.action == "query"
	input.model.name in account_models
	count(finance_only_requested) > 0
	is_finance
}

finance_only_requested contains m if {
	some m in input.request.metrics
	m in finance_only_metrics
}

# Header auth carries the role. JWT auth (Keycloak) also carries groups.
is_finance if "finance_analyst" in input.identity.roles
is_finance if "finance" in input.identity.groups
