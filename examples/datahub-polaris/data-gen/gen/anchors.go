package gen

import (
	"time"

	"chd.local/datagen/constants"
)

// AccountAnchor fixes a company fixture used across independently generated facts.
type AccountAnchor struct {
	AccountID       int64
	AccountName     string
	Segment         string
	Region          string
	Industry        string
	CSMTeam         string
	RenewalDate     time.Time
	LifecycleStatus string
}

// ContactAnchor contains synthetic-only PII reserved for the Northstar fixture.
type ContactAnchor struct {
	AccountID int64
	FullName  string
	Email     string
	Phone     string
	JobTitle  string
}

// EntitlementAnchor fixes low-adoption licensed capacity for one feature.
type EntitlementAnchor struct {
	AccountID     int64
	FeatureID     int
	LicensedSeats int
	EligibleSeats int
	AdoptedSeats  int
}

var (
	Northstar = AccountAnchor{
		AccountID: 1001, AccountName: "Northstar Systems", Segment: "Enterprise",
		Region: "NA", Industry: "Technology", CSMTeam: "Enterprise North America",
		RenewalDate: utcDate(2026, time.September, 15), LifecycleStatus: "renewal-due",
	}
	NorthstarContact = ContactAnchor{
		AccountID: 1001, FullName: "Avery Chen", Email: "avery.chen@northstar.example",
		Phone: "+1-555-010-1001", JobTitle: "VP, Customer Operations",
	}
	NorthstarEntitlements = []EntitlementAnchor{
		{1001, 1, 500, 450, 54},
		{1001, 2, 500, 420, 38},
		{1001, 3, 350, 300, 21},
		{1001, 4, 300, 260, 13},
		{1001, 5, 200, 180, 7},
		{1001, 6, 150, 120, 3},
	}
)

func utcDate(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// SubscriptionAnchor fixes a subscription trajectory used by revenue metrics.
type SubscriptionAnchor struct {
	SubscriptionID   int64
	AccountID        int64
	PlanID           int
	ContractID       int64
	BaselineMRRCents int64
	CurrentMRRCents  int64
}

var NorthstarSubscriptions = []SubscriptionAnchor{
	{100_001, 1001, 12, 50_001, 5_000_000, 4_500_000},
	{0, 1001, 8, 50_001, 0, 1_200_000},
}

// init derives the Northstar account's second subscription id. The round-robin
// account assignment returns to account 1001 at loop index == account count, so
// its subscription_id is account-count dependent (100061 at 60 accounts).
func init() {
	if len(NorthstarSubscriptions) > 1 {
		NorthstarSubscriptions[1].SubscriptionID = firstSubscriptionID + int64(constants.ExpectedTableRowCounts["account"])
	}
}

func northstarSubscription(id int64) (SubscriptionAnchor, bool) {
	for _, subscription := range NorthstarSubscriptions {
		if subscription.SubscriptionID == id {
			return subscription, true
		}
	}
	return SubscriptionAnchor{}, false
}

// SupportAnchor fixes cutoff-evaluated support facts for Northstar.
type SupportAnchor struct {
	TotalTickets     int
	CurrentTickets   int
	CurrentOpen      int
	CurrentEscalated int
	CurrentSLAMet    int
}

var NorthstarSupport = SupportAnchor{
	TotalTickets: 600, CurrentTickets: 600, CurrentOpen: 120,
	CurrentEscalated: 150, CurrentSLAMet: 400,
}
