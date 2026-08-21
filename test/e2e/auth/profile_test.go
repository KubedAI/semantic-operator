//go:build e2e

package authe2e

// profile is the single source of truth for one engine's e2e shape: what the
// harness queries and asserts, and how the orchestrator prepares the engine.
// Deploy details (values files, model manifests, credentials) live in the bash
// primitives the orchestrator calls, not here, so this stays about the test.
type profile struct {
	engine    string
	modelName string // ossie model name, used in the query URL

	metric   string // a certified metric on the identity model
	allowDim string // a dimension not on the denied table
	denyDim  string // a dimension on the denied table
	maskDim  string // the masked column, or "" when the engine cannot mask

	// setup is the make target (and args) that prepares the engine, data, and
	// per-user grants before the identity-mode releases are deployed. Trino's
	// masking dataset is the built-in tpch connector, so it needs no load;
	// StarRocks needs the retail data and grants from models-deploy.
	setup []string
}

var profiles = map[string]profile{
	"trino": {
		engine:    "trino",
		modelName: "tpch_orders_model",
		metric:    "total_price",
		allowDim:  "orders.clerk",
		denyDim:   "customer.mktsegment",
		maskDim:   "orders.clerk",
		setup:     []string{"trino-deploy"},
	},
	"starrocks": {
		engine:    "starrocks",
		modelName: "retail_identity_model",
		metric:    "total_sales",
		allowDim:  "store.s_store_name",
		denyDim:   "customer.c_birth_year",
		maskDim:   "", // StarRocks has no column masking; denial only.
		setup:     []string{"models-deploy", "KIND_ENGINE_TYPE=starrocks"},
	},
}
