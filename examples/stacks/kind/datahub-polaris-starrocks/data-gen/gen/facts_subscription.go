package gen

import (
	"fmt"
	"math/rand/v2"
	"time"

	"account-demo.local/datagen/constants"
)

const (
	firstSubscriptionID   int64 = 100_001
	baselineSnapshotIndex       = 11
	currentSnapshotIndex        = constants.SubscriptionSnapshotCount - 1

	cohortChurnSubscriptionID = firstSubscriptionID + 1
	newSubscriptionID         = firstSubscriptionID + 2
	contractionSubscriptionID = firstSubscriptionID + 3
	expansionSubscriptionID   = firstSubscriptionID + 4
)

type subscriptionContract struct {
	id        int64
	accountID int64
	end       time.Time
}

type subscriptionProfile struct {
	startIndex       int
	churnIndex       int
	pauseStart       int
	pauseEnd         int
	changeIndex      int
	baselineMRRCents int64
	currentMRRCents  int64
}

type currentSubscriptionFields struct {
	accountID       any
	mrrCents        int64
	arrCents        int64
	renewalARRCents int64
	grrStartCents   int64
	grrRetained     int64
	nrrStartCents   int64
	nrrEndingCents  int64
}

func generateSubscriptions(rng *rand.Rand, emit func(Row) error) error {
	contracts, err := subscriptionContracts()
	if err != nil {
		return err
	}
	subscriptionCount := constants.ExpectedTableRowCounts["subscription_monthly"] / constants.SubscriptionSnapshotCount
	if len(contracts) < subscriptionCount {
		return fmt.Errorf("contracts = %d, need at least %d for subscriptions", len(contracts), subscriptionCount)
	}

	for i := 0; i < subscriptionCount; i++ {
		subscriptionID := firstSubscriptionID + int64(i)
		contract := contracts[i]
		planID := i%constants.ExpectedTableRowCounts["plan"] + 1
		profile := randomSubscriptionProfile(rng, i)

		if anchor, ok := northstarSubscription(subscriptionID); ok {
			linked, ok := contractByID(contracts, anchor.ContractID)
			if !ok {
				return fmt.Errorf("Northstar subscription %d contract %d not generated", subscriptionID, anchor.ContractID)
			}
			contract = linked
			planID = anchor.PlanID
			profile = anchorSubscriptionProfile(anchor)
		}
		if contract.accountID != subscriptionAccountID(i) {
			return fmt.Errorf("subscription %d account %d does not match contract %d account %d", subscriptionID, subscriptionAccountID(i), contract.id, contract.accountID)
		}

		baselineMRR := profile.mrrAt(baselineSnapshotIndex)
		cohort := baselineMRR > 0
		for monthIndex, snapshot := range constants.SubscriptionSnapshotMonths {
			mrrCents := profile.mrrAt(monthIndex)
			status := profile.statusAt(monthIndex, contract.end)
			current := projectCurrentSubscription(snapshot, contract.accountID, mrrCents, baselineMRR, cohort, contract.end)
			row := Row{
				subscriptionID, dateSK(snapshot), contract.accountID, planID, contract.id,
				status, cents(mrrCents), current.accountID, cents(current.mrrCents),
				cents(current.arrCents), cents(current.renewalARRCents),
				cents(current.grrStartCents), cents(current.grrRetained),
				cents(current.nrrStartCents), cents(current.nrrEndingCents),
			}
			if err := emit(row); err != nil {
				return err
			}
		}
	}
	return nil
}

// subscriptionContracts consumes the contract generator's own deterministic
// stream. Subscription linkage therefore exactly matches generated contract
// rows without sharing or advancing the subscription stream.
func subscriptionContracts() ([]subscriptionContract, error) {
	contracts := make([]subscriptionContract, 0, constants.ExpectedTableRowCounts["contract"])
	err := generateContracts(tableRand("contract"), func(row Row) error {
		contractID, ok := row[0].(int64)
		if !ok {
			return fmt.Errorf("contract id has type %T", row[0])
		}
		accountID, ok := row[1].(int64)
		if !ok {
			return fmt.Errorf("contract account id has type %T", row[1])
		}
		end, ok := row[3].(time.Time)
		if !ok {
			return fmt.Errorf("contract end has type %T", row[3])
		}
		contracts = append(contracts, subscriptionContract{id: contractID, accountID: accountID, end: end})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("derive contract linkage: %w", err)
	}
	return contracts, nil
}

func contractByID(contracts []subscriptionContract, id int64) (subscriptionContract, bool) {
	index := id - 50_001
	if index < 0 || index >= int64(len(contracts)) || contracts[index].id != id {
		return subscriptionContract{}, false
	}
	return contracts[index], true
}

func subscriptionAccountID(index int) int64 {
	return firstAccountID + int64(index%constants.ExpectedTableRowCounts["account"])
}

func randomSubscriptionProfile(rng *rand.Rand, index int) subscriptionProfile {
	base := int64(50_000 + rng.Uint64N(4_950_001))
	profile := subscriptionProfile{
		startIndex:       int(rng.Uint64N(7)),
		churnIndex:       -1,
		pauseStart:       -1,
		pauseEnd:         -1,
		changeIndex:      18,
		baselineMRRCents: base,
		currentMRRCents:  base,
	}

	switch index % 10 {
	case 1:
		profile.churnIndex = 18 + int(rng.Uint64N(5))
		profile.currentMRRCents = 0
	case 2:
		profile.startIndex = 12 + int(rng.Uint64N(7))
		profile.baselineMRRCents = 0
	case 3:
		profile.currentMRRCents = base * int64(55+rng.Uint64N(36)) / 100
	case 4:
		profile.currentMRRCents = base * int64(110+rng.Uint64N(91)) / 100
	case 5:
		profile.pauseStart = 20
		profile.pauseEnd = constants.SubscriptionSnapshotCount
		profile.currentMRRCents = 0
	case 6:
		profile.churnIndex = 7 + int(rng.Uint64N(4))
		profile.baselineMRRCents = 0
		profile.currentMRRCents = 0
	case 7:
		profile.startIndex = constants.SubscriptionSnapshotCount
		profile.baselineMRRCents = 0
		profile.currentMRRCents = 0
	}

	// Stable edge fixtures exercise cohort semantics independently of random
	// distribution changes.
	switch firstSubscriptionID + int64(index) {
	case cohortChurnSubscriptionID:
		profile = subscriptionProfile{0, 18, -1, -1, 18, 2_000_000, 0}
	case newSubscriptionID:
		profile = subscriptionProfile{12, -1, -1, -1, 18, 0, 1_500_000}
	case contractionSubscriptionID:
		profile = subscriptionProfile{0, -1, -1, -1, 18, 2_000_000, 1_200_000}
	case expansionSubscriptionID:
		profile = subscriptionProfile{0, -1, -1, -1, 18, 2_000_000, 3_000_000}
	}
	return profile
}

func anchorSubscriptionProfile(anchor SubscriptionAnchor) subscriptionProfile {
	start := 0
	if anchor.BaselineMRRCents == 0 {
		start = baselineSnapshotIndex + 1
	}
	return subscriptionProfile{
		startIndex: start, churnIndex: -1, pauseStart: -1, pauseEnd: -1,
		changeIndex: 18, baselineMRRCents: anchor.BaselineMRRCents,
		currentMRRCents: anchor.CurrentMRRCents,
	}
}

func (p subscriptionProfile) mrrAt(monthIndex int) int64 {
	if monthIndex < p.startIndex || (p.churnIndex >= 0 && monthIndex >= p.churnIndex) ||
		(p.pauseStart >= 0 && monthIndex >= p.pauseStart && monthIndex < p.pauseEnd) {
		return 0
	}
	if monthIndex <= baselineSnapshotIndex || monthIndex < p.changeIndex {
		return p.baselineMRRCents
	}
	return p.currentMRRCents
}

func (p subscriptionProfile) statusAt(monthIndex int, contractEnd time.Time) string {
	if monthIndex < p.startIndex {
		return "trial"
	}
	if p.churnIndex >= 0 && monthIndex >= p.churnIndex {
		return "churned"
	}
	if p.pauseStart >= 0 && monthIndex >= p.pauseStart && monthIndex < p.pauseEnd {
		return "paused"
	}
	if monthIndex == currentSnapshotIndex && inRenewalHorizon(contractEnd) && p.mrrAt(monthIndex) > 0 {
		return "renewal-due"
	}
	return "active"
}

func projectCurrentSubscription(snapshot time.Time, accountID, mrrCents, baselineMRRCents int64, cohort bool, contractEnd time.Time) currentSubscriptionFields {
	var fields currentSubscriptionFields
	if !snapshot.Equal(constants.CurrentSnapshot) {
		return fields
	}
	if mrrCents > 0 {
		fields.accountID = accountID
		fields.mrrCents = mrrCents
		fields.arrCents = mrrCents * 12
		if inRenewalHorizon(contractEnd) {
			fields.renewalARRCents = fields.arrCents
		}
	}
	if cohort {
		fields.grrStartCents = baselineMRRCents
		fields.grrRetained = min(mrrCents, baselineMRRCents)
		fields.nrrStartCents = baselineMRRCents
		fields.nrrEndingCents = mrrCents
	}
	return fields
}

func inRenewalHorizon(end time.Time) bool {
	return !end.Before(constants.RenewalHorizonStart) && end.Before(constants.RenewalHorizonEnd)
}

func cents(value int64) float64 { return float64(value) / 100 }
