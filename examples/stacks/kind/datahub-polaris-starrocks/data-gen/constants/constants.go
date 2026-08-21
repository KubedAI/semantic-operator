// Package constants holds the versioned physical-data manifest for the local
// demo. Unlike the parent demo, there is a single fixed profile (the small
// ~44k-row dataset); the calendar, the plan/feature catalogs, and the
// Northstar anchor (account 1001) are the same as the reference dataset, and
// every generator invariant still holds exactly (see the gen package).
package constants

import "time"

const (
	// Seed makes every table's random stream deterministic. Changing it
	// changes the generated data.
	Seed int64 = 20260720
	// DatabaseName is the Iceberg namespace the tables live in.
	DatabaseName = "saas_accounts_demo"
	// SubscriptionSnapshotCount is the number of monthly subscription snapshots.
	SubscriptionSnapshotCount = 24
)

// Fixed window boundaries used by the point-in-time (current_*) projections.
var (
	AsOfDate               = date(2026, time.June, 30)
	DateRangeStart         = date(2024, time.July, 1)
	DateRangeEnd           = date(2027, time.June, 30)
	SnapshotRangeStart     = date(2024, time.July, 1)
	SnapshotRangeEnd       = date(2026, time.June, 1)
	CurrentSnapshot        = date(2026, time.June, 1)
	RetentionBaseline      = date(2025, time.June, 1)
	RenewalHorizonStart    = date(2026, time.July, 1)
	RenewalHorizonEnd      = date(2026, time.October, 1)
	AdoptionWindowStart    = date(2026, time.June, 1)
	AdoptionWindowEnd      = date(2026, time.July, 1)
	SupportWindowStart     = date(2026, time.April, 2)
	SupportWindowEnd       = date(2026, time.July, 1)
	SupportObservedThrough = date(2026, time.June, 28)

	SubscriptionSnapshotMonths = monthStarts(SnapshotRangeStart, SubscriptionSnapshotCount)
)

// ExpectedTableRowCounts is the fixed small profile (~44,835 business rows).
// Fact volume scales with the 60-account base. Invariants that the generators
// enforce and rely on:
//   - account_feature_entitlement / account == 6 (the Northstar anchor covers
//     features 1..6)
//   - subscription_monthly / SubscriptionSnapshotCount(24) == 70 subscriptions,
//     <= contract(80), and > account(60) so the wrap-around Northstar
//     subscription at loop index 60 is generated
//   - usage_daily / account / 120 adoption-window days == 5 rows/account/day
//   - support_ticket >= 600 (the fixed Northstar support fixture)
//   - account_feature_monthly == account_feature_entitlement * 4 months
var ExpectedTableRowCounts = map[string]int{
	"date_dim":                    1_095,
	"account":                     60,
	"account_primary_contact":     60,
	"plan":                        12,
	"contract":                    80,
	"product_feature":             48,
	"account_feature_entitlement": 360,
	"subscription_monthly":        1_680,
	"usage_daily":                 36_000,
	"support_ticket":              4_000,
	"account_feature_monthly":     1_440,
}

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func monthStarts(start time.Time, count int) []time.Time {
	months := make([]time.Time, count)
	for i := range months {
		months[i] = start.AddDate(0, i, 0)
	}
	return months
}
