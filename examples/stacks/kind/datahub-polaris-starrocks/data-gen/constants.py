"""Versioned physical-data manifest for the local demo.

There is a single fixed profile: the small (~44k-row) dataset. The calendar,
the plan and feature catalogs, and the Northstar anchor (account 1001) are the
same as the reference dataset. Every generator invariant still holds exactly
(see the generators module).
"""

import datetime

# Seed makes every table's random stream deterministic. Changing it changes the
# generated data.
SEED = 20260720

# DATABASE_NAME is the Iceberg namespace the tables live in.
DATABASE_NAME = "saas_accounts_demo"

# SUBSCRIPTION_SNAPSHOT_COUNT is the number of monthly subscription snapshots.
SUBSCRIPTION_SNAPSHOT_COUNT = 24


def _date(year, month, day):
    return datetime.date(year, month, day)


# Fixed window boundaries used by the point-in-time (current_*) projections.
AS_OF_DATE = _date(2026, 6, 30)
DATE_RANGE_START = _date(2024, 7, 1)
DATE_RANGE_END = _date(2027, 6, 30)
SNAPSHOT_RANGE_START = _date(2024, 7, 1)
SNAPSHOT_RANGE_END = _date(2026, 6, 1)
CURRENT_SNAPSHOT = _date(2026, 6, 1)
RETENTION_BASELINE = _date(2025, 6, 1)
RENEWAL_HORIZON_START = _date(2026, 7, 1)
RENEWAL_HORIZON_END = _date(2026, 10, 1)
ADOPTION_WINDOW_START = _date(2026, 6, 1)
ADOPTION_WINDOW_END = _date(2026, 7, 1)
SUPPORT_WINDOW_START = _date(2026, 4, 2)
SUPPORT_WINDOW_END = _date(2026, 7, 1)
SUPPORT_OBSERVED_THROUGH = _date(2026, 6, 28)

# ExpectedTableRowCounts is the fixed small profile (~44,835 business rows).
# Fact volume scales with the 60-account base. Invariants that the generators
# enforce and rely on:
#   - account_feature_entitlement / account == 6 (the Northstar anchor covers
#     features 1..6)
#   - subscription_monthly / SUBSCRIPTION_SNAPSHOT_COUNT(24) == 70 subscriptions,
#     <= contract(80), and > account(60) so the wrap-around Northstar
#     subscription at loop index 60 is generated
#   - usage_daily / account / 120 adoption-window days == 5 rows/account/day
#   - support_ticket >= 600 (the fixed Northstar support fixture)
#   - account_feature_monthly == account_feature_entitlement * 4 months
EXPECTED_TABLE_ROW_COUNTS = {
    "date_dim": 1_095,
    "account": 60,
    "account_primary_contact": 60,
    "plan": 12,
    "contract": 80,
    "product_feature": 48,
    "account_feature_entitlement": 360,
    "subscription_monthly": 1_680,
    "usage_daily": 36_000,
    "support_ticket": 4_000,
    "account_feature_monthly": 1_440,
}


def add_date(base, years, months, days):
    """Mirror Go's time.AddDate: normalize months into the year, then add days.

    Go builds the date on the normalized year and month, then treats the day as
    an offset that rolls over. Starting from the first of the normalized month
    and adding (day - 1 + days) reproduces that behavior exactly.
    """
    total_months = (base.year + years) * 12 + (base.month - 1) + months
    year, month_index = divmod(total_months, 12)
    first = datetime.date(year, month_index + 1, 1)
    return first + datetime.timedelta(days=base.day - 1 + days)


def month_starts(start, count):
    return [add_date(start, 0, i, 0) for i in range(count)]


# The 24 monthly subscription snapshot dates, one per month from the range start.
SUBSCRIPTION_SNAPSHOT_MONTHS = month_starts(SNAPSHOT_RANGE_START, SUBSCRIPTION_SNAPSHOT_COUNT)
