package gen

// Column describes one physical Iceberg column. Type is the StarRocks DDL type
// consumed by the loader; Nullable is explicit so the model key columns stay
// NOT NULL.
type Column struct {
	Name     string
	Type     string
	Nullable bool
}

// TableSchema is the ordered physical column list for a generated table.
type TableSchema struct {
	Name    string
	Columns []Column
}

var schemas = map[string]TableSchema{
	"date_dim": tableSchema("date_dim", []Column{
		col("date_sk", "INT"), col("date_date", "DATE"), col("year", "INT"),
		col("quarter", "INT"), col("month", "INT"),
	}),
	"account": tableSchema("account", []Column{
		col("account_id", "BIGINT"), col("account_name", "VARCHAR(120)"),
		col("segment", "VARCHAR(32)"), col("region", "VARCHAR(16)"),
		col("industry", "VARCHAR(64)"), col("csm_team", "VARCHAR(64)"),
		col("renewal_date", "DATE"), col("lifecycle_status", "VARCHAR(24)"),
	}),
	"account_primary_contact": tableSchema("account_primary_contact", []Column{
		col("account_id", "BIGINT"), col("full_name", "VARCHAR(100)"),
		col("email", "VARCHAR(160)"), col("phone", "VARCHAR(32)"),
		col("job_title", "VARCHAR(100)"),
	}),
	"plan": tableSchema("plan", []Column{
		col("plan_id", "INT"), col("plan_name", "VARCHAR(80)"),
		col("plan_tier", "VARCHAR(32)"), col("product_family", "VARCHAR(64)"),
	}),
	"contract": tableSchema("contract", []Column{
		col("contract_id", "BIGINT"), col("account_id", "BIGINT"),
		col("contract_start", "DATE"), col("contract_end", "DATE"),
		col("negotiated_discount_pct", "DECIMAL(5,2)"),
		col("negotiated_annual_rate", "DECIMAL(14,2)"),
		col("contract_value", "DECIMAL(16,2)"),
	}),
	"product_feature": tableSchema("product_feature", []Column{
		col("feature_id", "INT"), col("feature_name", "VARCHAR(100)"),
		col("product_area", "VARCHAR(64)"), col("criticality", "VARCHAR(16)"),
	}),
	"account_feature_entitlement": tableSchema("account_feature_entitlement", []Column{
		col("account_id", "BIGINT"), col("feature_id", "INT"),
		col("licensed_seats", "INT"), col("eligible_seats", "INT"),
		col("adopted_seats", "INT"),
	}),
	"subscription_monthly": tableSchema("subscription_monthly", []Column{
		col("subscription_id", "BIGINT"), col("snapshot_date_sk", "INT"),
		col("account_id", "BIGINT"), col("plan_id", "INT"), col("contract_id", "BIGINT"),
		col("status", "VARCHAR(24)"), col("mrr", "DECIMAL(14,2)"),
		nullable("current_account_id", "BIGINT"), col("current_mrr_amount", "DECIMAL(14,2)"),
		col("current_arr_amount", "DECIMAL(16,2)"), col("current_renewal_arr_amount", "DECIMAL(16,2)"),
		col("current_grr_starting_mrr", "DECIMAL(14,2)"), col("current_grr_retained_mrr", "DECIMAL(14,2)"),
		col("current_nrr_starting_mrr", "DECIMAL(14,2)"), col("current_nrr_ending_mrr", "DECIMAL(14,2)"),
	}),
	"usage_daily": tableSchema("usage_daily", []Column{
		col("usage_event_id", "BIGINT"), col("account_id", "BIGINT"), col("user_id", "BIGINT"),
		col("feature_id", "INT"), col("usage_date_sk", "INT"), col("total_events", "INT"),
		col("error_events", "INT"), nullable("current_user_id", "BIGINT"),
		nullable("current_user_feature_id", "BIGINT"), nullable("current_account_id", "BIGINT"),
		nullable("current_usage_date_sk", "INT"), col("current_error_events", "INT"),
		col("current_total_events", "INT"),
	}),
	"support_ticket": tableSchema("support_ticket", []Column{
		col("ticket_id", "BIGINT"), col("account_id", "BIGINT"), nullable("feature_id", "INT"),
		col("created_date_sk", "INT"), col("requester_email", "VARCHAR(160)"),
		col("subject", "VARCHAR(200)"), col("status", "VARCHAR(24)"),
		col("escalated_flag", "INT"), col("sla_met_flag", "INT"),
		col("first_response_hours", "DECIMAL(10,2)"), nullable("resolution_hours", "DECIMAL(10,2)"),
		nullable("current_ticket_id", "BIGINT"), col("current_open_flag", "INT"),
		col("current_escalated_flag", "INT"), col("current_sla_met_flag", "INT"),
		nullable("current_first_response_hours", "DECIMAL(10,2)"),
		nullable("current_resolution_hours", "DECIMAL(10,2)"),
	}),
	"account_feature_monthly": tableSchema("account_feature_monthly", []Column{
		col("account_id", "BIGINT"), col("feature_id", "INT"), col("snapshot_date_sk", "INT"),
		col("licensed_seats", "INT"), col("eligible_seats", "INT"), col("adopted_seats", "INT"),
		col("active_users", "INT"), col("total_events", "BIGINT"),
	}),
}

// Schema returns a defensive copy. An unknown table returns the zero value.
func Schema(table string) TableSchema {
	schema, ok := schemas[table]
	if !ok {
		return TableSchema{}
	}
	schema.Columns = append([]Column(nil), schema.Columns...)
	return schema
}

func tableSchema(name string, columns []Column) TableSchema {
	return TableSchema{Name: name, Columns: columns}
}

func col(name, typ string) Column { return Column{Name: name, Type: typ} }

func nullable(name, typ string) Column { return Column{Name: name, Type: typ, Nullable: true} }
