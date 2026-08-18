package semantic.retail

import rego.v1

# This policy is an additive gate. The SemanticModel's built-in governance
# still denies customer email to analysts and applies the tx_analyst Texas row
# filter. OPA verifies the caller, operation, model, adapter, and basic request
# shape without duplicating those model-owned rules.
policy_revision := "retail-opa-v1"

allowed_roles := {"analyst", "tx_analyst", "admin"}

default allow := false

allow if {
	input.apiVersion == "authorization.semantic.ossie.io/v1alpha1"
	input.action == "query"
	input.identity.principal == "demo-user"

	some role in input.identity.roles
	role in allowed_roles

	input.model.namespace == "semantic-system"
	input.model.resource == "tpcds-retail"
	input.model.name == "tpcds_retail_model"
	count(input.request.metrics) > 0

	input.environment.adapter == "rest"
	input.environment.accessTimeUnixMilli > 0
}

decision := {
	"allow": allow,
	"revision": policy_revision,
}
