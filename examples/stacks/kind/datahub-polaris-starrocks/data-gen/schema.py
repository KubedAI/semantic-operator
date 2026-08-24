"""Physical Iceberg column definitions for each generated table.

A Column is (name, type, nullable). Type is the StarRocks DDL type the loader
emits. Nullable is explicit so the model key columns stay NOT NULL. Each table's
column list is ordered to match the row tuples the generators emit.
"""

from collections import namedtuple

Column = namedtuple("Column", ["name", "type", "nullable"])


def _col(name, typ):
    return Column(name, typ, False)


def _nullable(name, typ):
    return Column(name, typ, True)


# TABLE_ORDER is the fixed generation and load order. It is shared so neither
# dict iteration nor load decisions can change output.
TABLE_ORDER = [
    "date_dim",
    "account",
    "account_primary_contact",
    "plan",
    "contract",
    "product_feature",
    "account_feature_entitlement",
    "subscription_monthly",
    "usage_daily",
    "support_ticket",
    "account_feature_monthly",
]

SCHEMAS = {
    "date_dim": [
        _col("date_sk", "INT"), _col("date_date", "DATE"), _col("year", "INT"),
        _col("quarter", "INT"), _col("month", "INT"),
    ],
    "account": [
        _col("account_id", "BIGINT"), _col("account_name", "VARCHAR(120)"),
        _col("segment", "VARCHAR(32)"), _col("region", "VARCHAR(16)"),
        _col("industry", "VARCHAR(64)"), _col("csm_team", "VARCHAR(64)"),
        _col("renewal_date", "DATE"), _col("lifecycle_status", "VARCHAR(24)"),
    ],
    "account_primary_contact": [
        _col("account_id", "BIGINT"), _col("full_name", "VARCHAR(100)"),
        _col("email", "VARCHAR(160)"), _col("phone", "VARCHAR(32)"),
        _col("job_title", "VARCHAR(100)"),
    ],
    "plan": [
        _col("plan_id", "INT"), _col("plan_name", "VARCHAR(80)"),
        _col("plan_tier", "VARCHAR(32)"), _col("product_family", "VARCHAR(64)"),
    ],
    "contract": [
        _col("contract_id", "BIGINT"), _col("account_id", "BIGINT"),
        _col("contract_start", "DATE"), _col("contract_end", "DATE"),
        _col("negotiated_discount_pct", "DECIMAL(5,2)"),
        _col("negotiated_annual_rate", "DECIMAL(14,2)"),
        _col("contract_value", "DECIMAL(16,2)"),
    ],
    "product_feature": [
        _col("feature_id", "INT"), _col("feature_name", "VARCHAR(100)"),
        _col("product_area", "VARCHAR(64)"), _col("criticality", "VARCHAR(16)"),
    ],
    "account_feature_entitlement": [
        _col("account_id", "BIGINT"), _col("feature_id", "INT"),
        _col("licensed_seats", "INT"), _col("eligible_seats", "INT"),
        _col("adopted_seats", "INT"),
    ],
    "subscription_monthly": [
        _col("subscription_id", "BIGINT"), _col("snapshot_date_sk", "INT"),
        _col("account_id", "BIGINT"), _col("plan_id", "INT"), _col("contract_id", "BIGINT"),
        _col("status", "VARCHAR(24)"), _col("mrr", "DECIMAL(14,2)"),
        _nullable("current_account_id", "BIGINT"), _col("current_mrr_amount", "DECIMAL(14,2)"),
        _col("current_arr_amount", "DECIMAL(16,2)"), _col("current_renewal_arr_amount", "DECIMAL(16,2)"),
        _col("current_grr_starting_mrr", "DECIMAL(14,2)"), _col("current_grr_retained_mrr", "DECIMAL(14,2)"),
        _col("current_nrr_starting_mrr", "DECIMAL(14,2)"), _col("current_nrr_ending_mrr", "DECIMAL(14,2)"),
    ],
    "usage_daily": [
        _col("usage_event_id", "BIGINT"), _col("account_id", "BIGINT"), _col("user_id", "BIGINT"),
        _col("feature_id", "INT"), _col("usage_date_sk", "INT"), _col("total_events", "INT"),
        _col("error_events", "INT"), _nullable("current_user_id", "BIGINT"),
        _nullable("current_user_feature_id", "BIGINT"), _nullable("current_account_id", "BIGINT"),
        _nullable("current_usage_date_sk", "INT"), _col("current_error_events", "INT"),
        _col("current_total_events", "INT"),
    ],
    "support_ticket": [
        _col("ticket_id", "BIGINT"), _col("account_id", "BIGINT"), _nullable("feature_id", "INT"),
        _col("created_date_sk", "INT"), _col("requester_email", "VARCHAR(160)"),
        _col("subject", "VARCHAR(200)"), _col("status", "VARCHAR(24)"),
        _col("escalated_flag", "INT"), _col("sla_met_flag", "INT"),
        _col("first_response_hours", "DECIMAL(10,2)"), _nullable("resolution_hours", "DECIMAL(10,2)"),
        _nullable("current_ticket_id", "BIGINT"), _col("current_open_flag", "INT"),
        _col("current_escalated_flag", "INT"), _col("current_sla_met_flag", "INT"),
        _nullable("current_first_response_hours", "DECIMAL(10,2)"),
        _nullable("current_resolution_hours", "DECIMAL(10,2)"),
    ],
    "account_feature_monthly": [
        _col("account_id", "BIGINT"), _col("feature_id", "INT"), _col("snapshot_date_sk", "INT"),
        _col("licensed_seats", "INT"), _col("eligible_seats", "INT"), _col("adopted_seats", "INT"),
        _col("active_users", "INT"), _col("total_events", "BIGINT"),
    ],
}


def schema(table):
    """Return the ordered column list for a table, or [] for an unknown table."""
    return list(SCHEMAS.get(table, []))
