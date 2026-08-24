"""Deterministic, streaming synthetic data for the local SaaS accounts demo.

Each table has an independent random stream, so loading or skipping one table
cannot change another table's rows. Row tuples are ordered to match the column
list in the schema module.

Determinism note: every table is generated from a stream seeded by
sha256(SEED, table_name), so a given seed always produces the same output and
the same row counts. The random *values* are not identical to the previous Go
implementation (Go used a PCG stream); the row counts, the structural
invariants, the fixed Northstar anchors, and the physical schema are preserved
exactly.
"""

import datetime
import hashlib
import math
import random

import constants
import anchors
import schema

FIRST_ACCOUNT_ID = 1001
FIRST_SUBSCRIPTION_ID = 100_001
FIRST_USAGE_EVENT_ID = 1_000_001
FIRST_SUPPORT_TICKET_ID = 2_000_001

# Subscription profile month indices.
BASELINE_SNAPSHOT_INDEX = 11
CURRENT_SNAPSHOT_INDEX = constants.SUBSCRIPTION_SNAPSHOT_COUNT - 1

# Stable subscription edge fixtures (ids relative to the first subscription id).
COHORT_CHURN_SUBSCRIPTION_ID = FIRST_SUBSCRIPTION_ID + 1
NEW_SUBSCRIPTION_ID = FIRST_SUBSCRIPTION_ID + 2
CONTRACTION_SUBSCRIPTION_ID = FIRST_SUBSCRIPTION_ID + 3
EXPANSION_SUBSCRIPTION_ID = FIRST_SUBSCRIPTION_ID + 4

SEGMENTS = ["Enterprise", "Mid-Market", "SMB"]
REGIONS = ["NA", "EMEA", "APAC", "LATAM"]
INDUSTRIES = ["Technology", "Financial Services", "Healthcare", "Retail",
              "Manufacturing", "Media", "Education", "Professional Services"]
CSM_TEAMS = ["Enterprise North America", "Enterprise International",
             "Growth North America", "Growth International", "Digital Success"]
LIFECYCLE_STATUSES = ["trial", "active", "paused", "churned", "renewal-due"]
COMPANY_PREFIXES = ["Acme", "Aperture", "Atlas", "Bluebird", "Cedar", "Evergreen",
                    "Harbor", "Juniper", "Keystone", "Lighthouse", "Meridian",
                    "Nimbus", "Orchard", "Pioneer", "Redwood", "Summit", "Vertex", "Willow"]
COMPANY_SUFFIXES = ["Analytics", "Cloud", "Dynamics", "Group", "Industries", "Labs",
                    "Logistics", "Networks", "Partners", "Services", "Software", "Works"]
FIRST_NAMES = ["Amara", "Diego", "Elena", "Hana", "Ibrahim", "Jordan", "Kai", "Leila",
               "Mateo", "Nia", "Priya", "Ravi", "Sofia", "Tariq", "Yuki", "Zoe"]
LAST_NAMES = ["Bennett", "Chen", "Diaz", "Fischer", "Gupta", "Haddad", "Ito", "Johnson",
              "Kim", "Lopez", "Mensah", "Novak", "Patel", "Rossi", "Singh", "Williams"]
JOB_TITLES = ["Chief Information Officer", "Director of Operations", "Head of Data",
              "IT Manager", "Revenue Operations Lead", "VP, Customer Experience",
              "VP, Engineering"]
SUPPORT_SUBJECTS = ["Dashboard access issue", "Automation run failed",
                    "Connector needs attention", "Export delayed", "Permission review",
                    "Workflow configuration question"]


# --- Random stream and small helpers ---------------------------------------

def _table_rand(table):
    """A random stream keyed to the seed and the table name."""
    digest = hashlib.sha256(f"{constants.SEED}\x00{table}".encode("utf-8")).digest()
    return random.Random(int.from_bytes(digest[:16], "little"))


def _uintn(rng, n):
    """Uniform integer in [0, n). Matches Go's Uint64N contract (n must be > 0)."""
    return rng.randrange(n)


def _weighted_index(rng, weights):
    total = sum(weights)
    value = _uintn(rng, total)
    for i, weight in enumerate(weights):
        if value < weight:
            return i
        value -= weight
    raise AssertionError("unreachable weighted index")


def _round_money(value):
    # Round half away from zero, like Go's math.Round; all money here is >= 0.
    return math.floor(value * 100 + 0.5) / 100


def _cents(value):
    return value / 100


def _bool_int(value):
    return 1 if value else 0


def date_sk(day):
    return day.year * 10_000 + day.month * 100 + day.day


def _days_inclusive(start, end):
    day = start
    step = datetime.timedelta(days=1)
    while day <= end:
        yield day
        day += step


def _in_half_open_window(value, start, end):
    return start <= value < end


def _count(table):
    return constants.EXPECTED_TABLE_ROW_COUNTS[table]


# --- Dimension generators ---------------------------------------------------

def generate_dates(_rng):
    for day in _days_inclusive(constants.DATE_RANGE_START, constants.DATE_RANGE_END):
        quarter = (day.month - 1) // 3 + 1
        yield [date_sk(day), day, day.year, quarter, day.month]


def generate_accounts(rng):
    count = _count("account")
    span = (constants.DATE_RANGE_END - constants.AS_OF_DATE).days
    for i in range(count):
        account_id = FIRST_ACCOUNT_ID + i
        if account_id == anchors.NORTHSTAR.account_id:
            yield _account_anchor_row(anchors.NORTHSTAR)
            continue
        headline = anchors.headline_account(account_id)
        if headline is not None:
            yield _account_anchor_row(headline)
            continue
        segment = SEGMENTS[_weighted_index(rng, [18, 32, 50])]
        region = REGIONS[_weighted_index(rng, [48, 27, 18, 7])]
        status = LIFECYCLE_STATUSES[_weighted_index(rng, [8, 62, 8, 10, 12])]
        team = CSM_TEAMS[4]
        if segment == "Enterprise":
            team = CSM_TEAMS[0] if region == "NA" else CSM_TEAMS[1]
        elif segment == "Mid-Market":
            team = CSM_TEAMS[2] if region == "NA" else CSM_TEAMS[3]
        renewal = constants.AS_OF_DATE + datetime.timedelta(days=_uintn(rng, span + 365) - 180)
        if status == "renewal-due":
            days = (constants.RENEWAL_HORIZON_END - constants.RENEWAL_HORIZON_START).days
            renewal = constants.RENEWAL_HORIZON_START + datetime.timedelta(days=_uintn(rng, days))
        prefix = COMPANY_PREFIXES[_uintn(rng, len(COMPANY_PREFIXES))]
        suffix = COMPANY_SUFFIXES[_uintn(rng, len(COMPANY_SUFFIXES))]
        name = f"{prefix} {suffix} {account_id:04d}"
        industry = INDUSTRIES[_uintn(rng, len(INDUSTRIES))]
        yield [account_id, name, segment, region, industry, team, renewal, status]


def generate_contacts(rng):
    count = _count("account_primary_contact")
    for i in range(count):
        account_id = FIRST_ACCOUNT_ID + i
        if account_id == anchors.NORTHSTAR_CONTACT.account_id:
            c = anchors.NORTHSTAR_CONTACT
            yield [account_id, c.full_name, c.email, c.phone, c.job_title]
            continue
        first = FIRST_NAMES[_uintn(rng, len(FIRST_NAMES))]
        last = LAST_NAMES[_uintn(rng, len(LAST_NAMES))]
        email = f"{first.lower()}.{last.lower()}.{account_id}@customer.example"
        phone = f"+1-555-{(account_id // 10) % 1000:03d}-{account_id % 10000:04d}"
        title = JOB_TITLES[_uintn(rng, len(JOB_TITLES))]
        yield [account_id, f"{first} {last}", email, phone, title]


def generate_plans(_rng):
    families = ["Engage", "Insights", "Platform"]
    tiers = ["Starter", "Growth", "Business", "Enterprise"]
    plan_id = 1
    for family in families:
        for tier in tiers:
            yield [plan_id, f"{family} {tier}", tier, family]
            plan_id += 1


def generate_contracts(rng):
    count = _count("contract")
    account_count = _count("account")
    for i in range(count):
        contract_id = 50_001 + i
        account_id = FIRST_ACCOUNT_ID + (i % account_count)
        start = datetime.date(2024, 1, 1) + datetime.timedelta(days=_uintn(rng, 730))
        term_months = [12, 12, 12, 24, 36][_uintn(rng, 5)]
        end = constants.add_date(start, 0, term_months, -1)
        # A headline account's first contract (i < account_count means the first
        # pass over the accounts) gets a fixed, above-ceiling value so the
        # leaderboard by contract value is deterministic, Northstar first.
        headline = anchors.headline_contract(account_id) if i < account_count else None
        if headline is not None:
            term_months = headline.term_months
            if account_id == anchors.NORTHSTAR.account_id:
                # Keep Northstar's renewal date as the contract end so the
                # point-in-time renewal semantics stay intact.
                end = anchors.NORTHSTAR.renewal_date
                start = constants.add_date(end, 0, -term_months, 1)
            else:
                end = constants.add_date(start, 0, term_months, -1)
            discount = headline.discount_pct
            annual_rate = _round_money(headline.contract_value / (term_months / 12 * (1 - discount / 100)))
            contract_value = _round_money(annual_rate * term_months / 12 * (1 - discount / 100))
        else:
            # Discounts run 5.00..40.00 percent so no contract shows an
            # unrealistically small discount.
            discount = _round_money(5 + _uintn(rng, 3501) / 100)
            annual_rate = _round_money(12_000 + _uintn(rng, 988_001))
            contract_value = _round_money(annual_rate * term_months / 12 * (1 - discount / 100))
        yield [contract_id, account_id, start, end, discount, annual_rate, contract_value]


def generate_features(_rng):
    areas = ["Analytics", "Automation", "Collaboration", "Data Platform",
             "Governance", "Integrations", "Security", "Workflow"]
    capabilities = ["Core", "Dashboards", "Exports", "Monitoring", "Orchestration", "Studio"]
    criticalities = ["low", "medium", "high", "critical"]
    feature_id = 1
    for area in areas:
        for j, capability in enumerate(capabilities):
            criticality = criticalities[(feature_id + j) % len(criticalities)]
            yield [feature_id, f"{area} {capability}", area, criticality]
            feature_id += 1


def generate_entitlements(rng):
    account_count = _count("account")
    feature_count = _count("product_feature")
    per_account = _count("account_feature_entitlement") // account_count
    anchor_by_feature = {e.feature_id: e for e in anchors.NORTHSTAR_ENTITLEMENTS}
    for i in range(account_count):
        account_id = FIRST_ACCOUNT_ID + i
        for j in range(per_account):
            feature_id = (i * per_account + j) % feature_count + 1
            if account_id == anchors.NORTHSTAR.account_id:
                anchor = anchor_by_feature.get(feature_id)
                if anchor is None:
                    raise ValueError(f"Northstar entitlement missing for feature {feature_id}")
                yield [anchor.account_id, anchor.feature_id, anchor.licensed_seats,
                       anchor.eligible_seats, anchor.adopted_seats]
                continue
            licensed = 10 + _uintn(rng, 991)
            eligible = 1 + _uintn(rng, licensed)
            adopted = _uintn(rng, eligible + 1)
            yield [account_id, feature_id, licensed, eligible, adopted]


def _account_anchor_row(a):
    return [a.account_id, a.account_name, a.segment, a.region,
            a.industry, a.csm_team, a.renewal_date, a.lifecycle_status]


# --- Subscription facts -----------------------------------------------------

class _SubscriptionProfile:
    __slots__ = ("start_index", "churn_index", "pause_start", "pause_end",
                 "change_index", "baseline_mrr_cents", "current_mrr_cents")

    def __init__(self, start_index, churn_index, pause_start, pause_end,
                 change_index, baseline_mrr_cents, current_mrr_cents):
        self.start_index = start_index
        self.churn_index = churn_index
        self.pause_start = pause_start
        self.pause_end = pause_end
        self.change_index = change_index
        self.baseline_mrr_cents = baseline_mrr_cents
        self.current_mrr_cents = current_mrr_cents

    def mrr_at(self, month_index):
        if (month_index < self.start_index
                or (self.churn_index >= 0 and month_index >= self.churn_index)
                or (self.pause_start >= 0 and self.pause_start <= month_index < self.pause_end)):
            return 0
        if month_index <= BASELINE_SNAPSHOT_INDEX or month_index < self.change_index:
            return self.baseline_mrr_cents
        return self.current_mrr_cents

    def status_at(self, month_index, contract_end):
        if month_index < self.start_index:
            return "trial"
        if self.churn_index >= 0 and month_index >= self.churn_index:
            return "churned"
        if self.pause_start >= 0 and self.pause_start <= month_index < self.pause_end:
            return "paused"
        if (month_index == CURRENT_SNAPSHOT_INDEX
                and _in_renewal_horizon(contract_end)
                and self.mrr_at(month_index) > 0):
            return "renewal-due"
        return "active"


class _CurrentSubscriptionFields:
    __slots__ = ("account_id", "mrr_cents", "arr_cents", "renewal_arr_cents",
                 "grr_start_cents", "grr_retained", "nrr_start_cents", "nrr_ending_cents")

    def __init__(self):
        self.account_id = None
        self.mrr_cents = 0
        self.arr_cents = 0
        self.renewal_arr_cents = 0
        self.grr_start_cents = 0
        self.grr_retained = 0
        self.nrr_start_cents = 0
        self.nrr_ending_cents = 0


def generate_subscriptions(rng):
    contracts = _subscription_contracts()
    subscription_count = _count("subscription_monthly") // constants.SUBSCRIPTION_SNAPSHOT_COUNT
    if len(contracts) < subscription_count:
        raise ValueError(
            f"contracts = {len(contracts)}, need at least {subscription_count} for subscriptions")

    for i in range(subscription_count):
        subscription_id = FIRST_SUBSCRIPTION_ID + i
        contract = contracts[i]
        plan_id = i % _count("plan") + 1
        profile = _random_subscription_profile(rng, i)

        anchor = anchors.northstar_subscription(subscription_id)
        if anchor is not None:
            linked = _contract_by_id(contracts, anchor.contract_id)
            if linked is None:
                raise ValueError(
                    f"Northstar subscription {subscription_id} contract {anchor.contract_id} not generated")
            contract = linked
            plan_id = anchor.plan_id
            profile = _anchor_subscription_profile(anchor)
        if contract["account_id"] != _subscription_account_id(i):
            raise ValueError(
                f"subscription {subscription_id} account {_subscription_account_id(i)} "
                f"does not match contract {contract['id']} account {contract['account_id']}")

        baseline_mrr = profile.mrr_at(BASELINE_SNAPSHOT_INDEX)
        cohort = baseline_mrr > 0
        for month_index, snapshot in enumerate(constants.SUBSCRIPTION_SNAPSHOT_MONTHS):
            mrr_cents = profile.mrr_at(month_index)
            status = profile.status_at(month_index, contract["end"])
            current = _project_current_subscription(
                snapshot, contract["account_id"], mrr_cents, baseline_mrr, cohort, contract["end"])
            yield [
                subscription_id, date_sk(snapshot), contract["account_id"], plan_id, contract["id"],
                status, _cents(mrr_cents), current.account_id, _cents(current.mrr_cents),
                _cents(current.arr_cents), _cents(current.renewal_arr_cents),
                _cents(current.grr_start_cents), _cents(current.grr_retained),
                _cents(current.nrr_start_cents), _cents(current.nrr_ending_cents),
            ]


def _subscription_contracts():
    """Consume the contract generator's own deterministic stream so linkage
    exactly matches generated contract rows without advancing any other stream.
    """
    contracts = []
    for row in generate_contracts(_table_rand("contract")):
        contracts.append({"id": row[0], "account_id": row[1], "end": row[3]})
    return contracts


def _contract_by_id(contracts, contract_id):
    index = contract_id - 50_001
    if index < 0 or index >= len(contracts) or contracts[index]["id"] != contract_id:
        return None
    return contracts[index]


def _subscription_account_id(index):
    return FIRST_ACCOUNT_ID + index % _count("account")


def _random_subscription_profile(rng, index):
    base = 50_000 + _uintn(rng, 4_950_001)
    profile = _SubscriptionProfile(
        start_index=_uintn(rng, 7), churn_index=-1, pause_start=-1, pause_end=-1,
        change_index=18, baseline_mrr_cents=base, current_mrr_cents=base)

    bucket = index % 10
    if bucket == 1:
        profile.churn_index = 18 + _uintn(rng, 5)
        profile.current_mrr_cents = 0
    elif bucket == 2:
        profile.start_index = 12 + _uintn(rng, 7)
        profile.baseline_mrr_cents = 0
    elif bucket == 3:
        profile.current_mrr_cents = base * (55 + _uintn(rng, 36)) // 100
    elif bucket == 4:
        profile.current_mrr_cents = base * (110 + _uintn(rng, 91)) // 100
    elif bucket == 5:
        profile.pause_start = 20
        profile.pause_end = constants.SUBSCRIPTION_SNAPSHOT_COUNT
        profile.current_mrr_cents = 0
    elif bucket == 6:
        profile.churn_index = 7 + _uintn(rng, 4)
        profile.baseline_mrr_cents = 0
        profile.current_mrr_cents = 0
    elif bucket == 7:
        profile.start_index = constants.SUBSCRIPTION_SNAPSHOT_COUNT
        profile.baseline_mrr_cents = 0
        profile.current_mrr_cents = 0

    # Stable edge fixtures exercise cohort semantics independently of random
    # distribution changes.
    subscription_id = FIRST_SUBSCRIPTION_ID + index
    if subscription_id == COHORT_CHURN_SUBSCRIPTION_ID:
        profile = _SubscriptionProfile(0, 18, -1, -1, 18, 2_000_000, 0)
    elif subscription_id == NEW_SUBSCRIPTION_ID:
        profile = _SubscriptionProfile(12, -1, -1, -1, 18, 0, 1_500_000)
    elif subscription_id == CONTRACTION_SUBSCRIPTION_ID:
        profile = _SubscriptionProfile(0, -1, -1, -1, 18, 2_000_000, 1_200_000)
    elif subscription_id == EXPANSION_SUBSCRIPTION_ID:
        profile = _SubscriptionProfile(0, -1, -1, -1, 18, 2_000_000, 3_000_000)
    return profile


def _anchor_subscription_profile(anchor):
    start = 0
    if anchor.baseline_mrr_cents == 0:
        start = BASELINE_SNAPSHOT_INDEX + 1
    return _SubscriptionProfile(
        start_index=start, churn_index=-1, pause_start=-1, pause_end=-1,
        change_index=18, baseline_mrr_cents=anchor.baseline_mrr_cents,
        current_mrr_cents=anchor.current_mrr_cents)


def _project_current_subscription(snapshot, account_id, mrr_cents, baseline_mrr_cents, cohort, contract_end):
    fields = _CurrentSubscriptionFields()
    if snapshot != constants.CURRENT_SNAPSHOT:
        return fields
    if mrr_cents > 0:
        fields.account_id = account_id
        fields.mrr_cents = mrr_cents
        fields.arr_cents = mrr_cents * 12
        if _in_renewal_horizon(contract_end):
            fields.renewal_arr_cents = fields.arr_cents
    if cohort:
        fields.grr_start_cents = baseline_mrr_cents
        fields.grr_retained = min(mrr_cents, baseline_mrr_cents)
        fields.nrr_start_cents = baseline_mrr_cents
        fields.nrr_ending_cents = mrr_cents
    return fields


def _in_renewal_horizon(end):
    return constants.RENEWAL_HORIZON_START <= end < constants.RENEWAL_HORIZON_END


# --- Account-feature monthly facts ------------------------------------------

def generate_account_feature_monthly(rng):
    months = 4
    entitlement_count = _count("account_feature_entitlement")
    if entitlement_count * months != _count("account_feature_monthly"):
        raise ValueError(
            f"monthly row manifest mismatch: entitlements={entitlement_count} "
            f"months={months} rows={_count('account_feature_monthly')}")
    month_start = constants.add_date(constants.CURRENT_SNAPSHOT, 0, -(months - 1), 0)
    for row in generate_entitlements(_table_rand("account_feature_entitlement")):
        account_id, feature_id, licensed, eligible, current_adopted = row
        for month_index in range(months):
            adopted, active, events = _monthly_adoption(
                rng, account_id, feature_id, month_index, current_adopted)
            if (adopted < 0 or adopted > eligible or active < 0
                    or active > adopted or events < active):
                raise ValueError(
                    f"invalid monthly adoption for account {account_id} feature {feature_id}: "
                    f"eligible={eligible} adopted={adopted} active={active} events={events}")
            snapshot = constants.add_date(month_start, 0, month_index, 0)
            yield [account_id, feature_id, date_sk(snapshot), licensed, eligible,
                   adopted, active, events]


def _monthly_adoption(rng, account_id, feature_id, month_index, current_adopted):
    if account_id == anchors.NORTHSTAR.account_id:
        adopted = current_adopted * (85 + month_index * 5) // 100
        if month_index == 3:
            adopted = current_adopted
        active, events = 0, 0
        if feature_id == 1:
            active = min(adopted, month_index + 1)
            events = 90 + month_index * 10
        elif feature_id == 2:
            active = min(adopted, 1 + month_index // 2)
            events = 30 + month_index * 10
        return adopted, active, events

    variation = 80 + month_index * 5 + _uintn(rng, 6)
    adopted = current_adopted * variation // 100
    if month_index == 3:
        adopted = current_adopted
    if current_adopted > 0 and adopted == 0:
        adopted = 1
    active = 0
    if adopted > 0:
        active = 1 + _uintn(rng, adopted)
    events = active * (1 + _uintn(rng, 121))
    return adopted, active, events


# --- Usage facts ------------------------------------------------------------

def generate_usage(rng):
    matches, features_by_account = _entitlement_reference()

    account_count = _count("account")
    # The span deliberately includes the day after the adoption window so the
    # half-open boundary is represented in physical data.
    usage_start = constants.add_date(constants.ADOPTION_WINDOW_START, 0, 0, -89)
    usage_end = constants.ADOPTION_WINDOW_END
    day_count = (usage_end - usage_start).days + 1
    rows_per_account_day = _count("usage_daily") // account_count // day_count
    if rows_per_account_day * account_count * day_count != _count("usage_daily"):
        raise ValueError(
            f"usage row manifest cannot be evenly streamed: accounts={account_count} "
            f"days={day_count} rows={_count('usage_daily')}")

    event_id = FIRST_USAGE_EVENT_ID
    for day in _days_inclusive(usage_start, usage_end):
        current = _in_half_open_window(day, constants.ADOPTION_WINDOW_START, constants.ADOPTION_WINDOW_END)
        for account_index in range(account_count):
            account_id = FIRST_ACCOUNT_ID + account_index
            features = features_by_account.get(account_id, [])
            if not features:
                raise ValueError(f"usage account {account_id} has no entitlements")
            for slot in range(rows_per_account_day):
                local_user, feature_id = _usage_assignment(account_id, slot, features)
                _require_exactly_one_entitlement(matches, account_id, feature_id, event_id)
                user_id = _usage_user_id(account_id, local_user)
                user_feature_id = _usage_user_feature_id(user_id, feature_id)
                total_events, error_events = _usage_counts(rng, account_id, slot, day)
                current_user_id = current_user_feature_id = None
                current_account_id = current_date_sk = None
                current_errors, current_events = 0, 0
                if current:
                    current_user_id = user_id
                    current_user_feature_id = user_feature_id
                    current_account_id = account_id
                    current_date_sk = date_sk(day)
                    current_errors = error_events
                    current_events = total_events
                yield [
                    event_id, account_id, user_id, feature_id, date_sk(day), total_events, error_events,
                    current_user_id, current_user_feature_id, current_account_id, current_date_sk,
                    current_errors, current_events,
                ]
                event_id += 1


def _entitlement_reference():
    matches = {}
    features_by_account = {}
    for row in generate_entitlements(_table_rand("account_feature_entitlement")):
        account_id, feature_id = row[0], row[1]
        key = (account_id, feature_id)
        matches[key] = matches.get(key, 0) + 1
        features_by_account.setdefault(account_id, []).append(feature_id)
    return matches, features_by_account


def _require_exactly_one_entitlement(matches, account_id, feature_id, event_id):
    count = matches.get((account_id, feature_id), 0)
    if count != 1:
        raise ValueError(
            f"usage event {event_id}: entitlement anti-join invariant: account {account_id} "
            f"feature {feature_id} matched {count} rows, want exactly 1")


def _usage_assignment(account_id, slot, entitled):
    if account_id == anchors.NORTHSTAR.account_id:
        users = [1, 2, 3, 4, 1]
        features = [1, 1, 2, 1, 2]
        return users[slot % len(users)], features[slot % len(features)]
    return slot + 1, entitled[slot % len(entitled)]


def _usage_user_id(account_id, local_user):
    # IDs are injective by construction: account occupies the high decimal
    # positions, local user the next positions, and feature (< 100) the low two.
    return account_id * 1_000 + local_user


def _usage_user_feature_id(user_id, feature_id):
    return user_id * 100 + feature_id


def _usage_counts(rng, account_id, slot, day):
    if account_id == anchors.NORTHSTAR.account_id:
        totals = [1, 1, 2, 1, 1]
        errors = 0
        if slot == 4 and day.day % 5 == 0:
            errors = 1
        return totals[slot % len(totals)], errors
    total = 1 + _uintn(rng, 80)
    errors = 0
    if _uintn(rng, 100) < 12:
        errors = 1 + _uintn(rng, min(total, 5))
    return total, errors


# --- Support facts ----------------------------------------------------------

def generate_support(rng):
    count = _count("support_ticket")
    account_count = _count("account")
    observed_days = (constants.SUPPORT_OBSERVED_THROUGH - constants.DATE_RANGE_START).days + 1
    current_days = (constants.SUPPORT_OBSERVED_THROUGH - constants.SUPPORT_WINDOW_START).days + 1
    if observed_days <= 0 or current_days <= 0:
        raise ValueError(
            f"invalid support date manifest: observed days={observed_days} current days={current_days}")

    total_tickets = anchors.NORTHSTAR_SUPPORT.total_tickets
    for i in range(count):
        ticket_id = FIRST_SUPPORT_TICKET_ID + i
        account_id = FIRST_ACCOUNT_ID + 1 + (i - total_tickets + account_count - 1) % (account_count - 1)
        created = constants.DATE_RANGE_START + datetime.timedelta(days=i % observed_days)
        feature_id = (i % _count("product_feature")) + 1
        requester = f"requester.{account_id}@customer.example"
        subject = SUPPORT_SUBJECTS[_uintn(rng, len(SUPPORT_SUBJECTS))]
        open_ticket = i % 9 == 0
        escalated = i % 17 == 0
        sla_met = i % 7 != 0

        if i < total_tickets:
            account_id = anchors.NORTHSTAR.account_id
            created = constants.SUPPORT_WINDOW_START + datetime.timedelta(days=i % current_days)
            if i % 3 == 0:
                feature_id = None
            else:
                feature_id = i % 2 + 1
            requester = anchors.NORTHSTAR_CONTACT.email
            subject = SUPPORT_SUBJECTS[i % len(SUPPORT_SUBJECTS)]
            open_ticket = i % 5 == 0
            escalated = i % 4 == 0
            sla_met = i % 3 != 0
        elif i % 5 == 0:
            feature_id = None

        status = "open" if open_ticket else "resolved"
        first_response = _support_first_response_hours(rng, sla_met)
        resolution = None
        if not open_ticket:
            resolution = _round_money(first_response + 2 + _uintn(rng, 2399) / 10)

        current_ticket_id = current_first_response = current_resolution = None
        current_open, current_escalated, current_sla_met = 0, 0, 0
        if _in_half_open_window(created, constants.SUPPORT_WINDOW_START, constants.SUPPORT_WINDOW_END):
            current_ticket_id = ticket_id
            current_open = _bool_int(open_ticket)
            current_escalated = _bool_int(escalated)
            current_sla_met = _bool_int(sla_met)
            current_first_response = first_response
            current_resolution = resolution

        if not str(subject).strip():
            raise ValueError(f"support ticket {ticket_id} has an empty subject")
        yield [
            ticket_id, account_id, feature_id, date_sk(created), requester, subject, status,
            _bool_int(escalated), _bool_int(sla_met), first_response, resolution,
            current_ticket_id, current_open, current_escalated, current_sla_met,
            current_first_response, current_resolution,
        ]


def _support_first_response_hours(rng, sla_met):
    if sla_met:
        return _round_money(0.25 + _uintn(rng, 350) / 100)
    return _round_money(4.25 + _uintn(rng, 1576) / 100)


# --- Dispatch ---------------------------------------------------------------

_GENERATORS = {
    "date_dim": generate_dates,
    "account": generate_accounts,
    "account_primary_contact": generate_contacts,
    "plan": generate_plans,
    "contract": generate_contracts,
    "product_feature": generate_features,
    "account_feature_entitlement": generate_entitlements,
    "subscription_monthly": generate_subscriptions,
    "usage_daily": generate_usage,
    "support_ticket": generate_support,
    "account_feature_monthly": generate_account_feature_monthly,
}


def tables():
    """Return the business tables in their fixed generation order."""
    return list(schema.TABLE_ORDER)


def rows(table, batch_size):
    """Yield lists of at most batch_size rows for a table, in generation order."""
    if batch_size <= 0:
        raise ValueError(f"batch size must be positive: {batch_size}")
    generate = _GENERATORS.get(table)
    if generate is None:
        raise ValueError(f"unknown table {table!r}")
    batch = []
    for row in generate(_table_rand(table)):
        batch.append(row)
        if len(batch) >= batch_size:
            yield batch
            batch = []
    if batch:
        yield batch
