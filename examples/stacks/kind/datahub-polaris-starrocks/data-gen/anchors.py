"""Fixed Northstar (account 1001) fixtures shared across independent facts.

These anchors are deterministic and do not depend on any random stream, so the
cohort and point-in-time semantics they exercise stay stable even if the random
distributions change.
"""

import datetime
from collections import namedtuple

import constants

AccountAnchor = namedtuple("AccountAnchor", [
    "account_id", "account_name", "segment", "region",
    "industry", "csm_team", "renewal_date", "lifecycle_status",
])

ContactAnchor = namedtuple("ContactAnchor", [
    "account_id", "full_name", "email", "phone", "job_title",
])

EntitlementAnchor = namedtuple("EntitlementAnchor", [
    "account_id", "feature_id", "licensed_seats", "eligible_seats", "adopted_seats",
])

SubscriptionAnchor = namedtuple("SubscriptionAnchor", [
    "subscription_id", "account_id", "plan_id", "contract_id",
    "baseline_mrr_cents", "current_mrr_cents",
])

SupportAnchor = namedtuple("SupportAnchor", [
    "total_tickets", "current_tickets", "current_open",
    "current_escalated", "current_sla_met",
])


def _utc_date(year, month, day):
    return datetime.date(year, month, day)


NORTHSTAR = AccountAnchor(
    account_id=1001, account_name="Northstar Systems", segment="Enterprise",
    region="NA", industry="Technology", csm_team="Enterprise North America",
    renewal_date=_utc_date(2026, 9, 15), lifecycle_status="renewal-due",
)

NORTHSTAR_CONTACT = ContactAnchor(
    account_id=1001, full_name="Avery Chen", email="avery.chen@northstar.example",
    phone="+1-555-010-1001", job_title="VP, Customer Operations",
)

NORTHSTAR_ENTITLEMENTS = [
    EntitlementAnchor(1001, 1, 500, 450, 54),
    EntitlementAnchor(1001, 2, 500, 420, 38),
    EntitlementAnchor(1001, 3, 350, 300, 21),
    EntitlementAnchor(1001, 4, 300, 260, 13),
    EntitlementAnchor(1001, 5, 200, 180, 7),
    EntitlementAnchor(1001, 6, 150, 120, 3),
]

FIRST_SUBSCRIPTION_ID = 100_001

# The round-robin account assignment returns to account 1001 at loop index equal
# to the account count, so the second Northstar subscription id is account-count
# dependent (100061 at 60 accounts).
_SECOND_SUBSCRIPTION_ID = FIRST_SUBSCRIPTION_ID + constants.EXPECTED_TABLE_ROW_COUNTS["account"]

NORTHSTAR_SUBSCRIPTIONS = [
    SubscriptionAnchor(100_001, 1001, 12, 50_001, 5_000_000, 4_500_000),
    SubscriptionAnchor(_SECOND_SUBSCRIPTION_ID, 1001, 8, 50_001, 0, 1_200_000),
]

NORTHSTAR_SUPPORT = SupportAnchor(
    total_tickets=600, current_tickets=600, current_open=120,
    current_escalated=150, current_sla_met=400,
)


def northstar_subscription(subscription_id):
    """Return the Northstar anchor for a subscription id, or None."""
    for subscription in NORTHSTAR_SUBSCRIPTIONS:
        if subscription.subscription_id == subscription_id:
            return subscription
    return None


# --- Headline leaderboard ---------------------------------------------------
#
# A fixed set of named accounts that always hold the largest contracts, so the
# "highest contract value" story is stable across regenerations. Northstar is
# always the largest. These account rows replace the random rows for their ids,
# and their first contract gets a fixed value from HEADLINE_CONTRACTS.

HEADLINE_ACCOUNTS = [
    AccountAnchor(
        account_id=1002, account_name="Aurora Dynamics", segment="Enterprise",
        region="EMEA", industry="Financial Services",
        csm_team="Enterprise International",
        renewal_date=_utc_date(2026, 6, 30), lifecycle_status="active",
    ),
    AccountAnchor(
        account_id=1003, account_name="Vanguard Networks", segment="Enterprise",
        region="APAC", industry="Manufacturing",
        csm_team="Enterprise International",
        renewal_date=_utc_date(2026, 11, 30), lifecycle_status="renewal-due",
    ),
    AccountAnchor(
        account_id=1004, account_name="Beacon Analytics", segment="Enterprise",
        region="NA", industry="Healthcare",
        csm_team="Enterprise North America",
        renewal_date=_utc_date(2026, 8, 31), lifecycle_status="active",
    ),
    AccountAnchor(
        account_id=1005, account_name="Sterling Industries", segment="Enterprise",
        region="LATAM", industry="Retail",
        csm_team="Enterprise International",
        renewal_date=_utc_date(2026, 10, 15), lifecycle_status="renewal-due",
    ),
]

ContractAnchor = namedtuple("ContractAnchor", [
    "account_id", "discount_pct", "term_months", "contract_value",
])

# The leaderboard by contract value. Northstar is #1. Every target value sits
# above the random ceiling: the maximum random annual rate is 1,000,000, over a
# 36-month term, at the 5.00 discount floor, which is 1,000,000 * 3 * 0.95 =
# 2,850,000. Every headline value below is above that, so ordering is stable.
HEADLINE_CONTRACTS = [
    ContractAnchor(account_id=1001, discount_pct=22.50, term_months=36, contract_value=3_600_000.00),
    ContractAnchor(account_id=1002, discount_pct=18.00, term_months=36, contract_value=3_500_000.00),
    ContractAnchor(account_id=1003, discount_pct=26.50, term_months=36, contract_value=3_400_000.00),
    ContractAnchor(account_id=1004, discount_pct=15.00, term_months=36, contract_value=3_300_000.00),
    ContractAnchor(account_id=1005, discount_pct=30.00, term_months=36, contract_value=3_200_000.00),
]


def headline_account(account_id):
    """Return the headline account anchor for an id, or None."""
    for account in HEADLINE_ACCOUNTS:
        if account.account_id == account_id:
            return account
    return None


def headline_contract(account_id):
    """Return the headline contract anchor for an account id, or None."""
    for contract in HEADLINE_CONTRACTS:
        if contract.account_id == account_id:
            return contract
    return None
