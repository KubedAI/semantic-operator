package gen

import (
	"fmt"
	"math/rand/v2"
	"time"

	"account-demo.local/datagen/constants"
)

const firstUsageEventID int64 = 1_000_001

type entitlementKey struct {
	accountID int64
	featureID int
}

func generateUsage(rng *rand.Rand, emit func(Row) error) error {
	matches, featuresByAccount, err := entitlementReference()
	if err != nil {
		return err
	}

	accountCount := constants.ExpectedTableRowCounts["account"]
	// The span deliberately includes the day after the adoption window so the
	// half-open boundary is represented in physical data.
	usageStart := constants.AdoptionWindowStart.AddDate(0, 0, -89)
	usageEnd := constants.AdoptionWindowEnd
	dayCount := int(usageEnd.Sub(usageStart).Hours()/24) + 1
	rowsPerAccountDay := constants.ExpectedTableRowCounts["usage_daily"] / accountCount / dayCount
	if rowsPerAccountDay*accountCount*dayCount != constants.ExpectedTableRowCounts["usage_daily"] {
		return fmt.Errorf("usage row manifest cannot be evenly streamed: accounts=%d days=%d rows=%d", accountCount, dayCount, constants.ExpectedTableRowCounts["usage_daily"])
	}

	eventID := firstUsageEventID
	for day := usageStart; !day.After(usageEnd); day = day.AddDate(0, 0, 1) {
		current := inHalfOpenWindow(day, constants.AdoptionWindowStart, constants.AdoptionWindowEnd)
		for accountIndex := 0; accountIndex < accountCount; accountIndex++ {
			accountID := firstAccountID + int64(accountIndex)
			features := featuresByAccount[accountID]
			if len(features) == 0 {
				return fmt.Errorf("usage account %d has no entitlements", accountID)
			}
			for slot := 0; slot < rowsPerAccountDay; slot++ {
				localUser, featureID := usageAssignment(accountID, slot, features)
				if err := requireExactlyOneEntitlement(matches, accountID, featureID); err != nil {
					return fmt.Errorf("usage event %d: %w", eventID, err)
				}
				userID := usageUserID(accountID, localUser)
				userFeatureID := usageUserFeatureID(userID, featureID)
				totalEvents, errorEvents := usageCounts(rng, accountID, slot, day)
				var currentUserID, currentUserFeatureID, currentAccountID, currentDateSK any
				currentErrors, currentEvents := 0, 0
				if current {
					currentUserID = userID
					currentUserFeatureID = userFeatureID
					currentAccountID = accountID
					currentDateSK = dateSK(day)
					currentErrors = errorEvents
					currentEvents = totalEvents
				}
				if err := emit(Row{
					eventID, accountID, userID, featureID, dateSK(day), totalEvents, errorEvents,
					currentUserID, currentUserFeatureID, currentAccountID, currentDateSK,
					currentErrors, currentEvents,
				}); err != nil {
					return err
				}
				eventID++
			}
		}
	}
	return nil
}

func entitlementReference() (map[entitlementKey]int, map[int64][]int, error) {
	matches := make(map[entitlementKey]int, constants.ExpectedTableRowCounts["account_feature_entitlement"])
	featuresByAccount := make(map[int64][]int, constants.ExpectedTableRowCounts["account"])
	err := generateEntitlements(tableRand("account_feature_entitlement"), func(row Row) error {
		accountID, ok := row[0].(int64)
		if !ok {
			return fmt.Errorf("entitlement account id has type %T", row[0])
		}
		featureID, ok := row[1].(int)
		if !ok {
			return fmt.Errorf("entitlement feature id has type %T", row[1])
		}
		key := entitlementKey{accountID, featureID}
		matches[key]++
		featuresByAccount[accountID] = append(featuresByAccount[accountID], featureID)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("derive entitlement reference: %w", err)
	}
	return matches, featuresByAccount, nil
}

func requireExactlyOneEntitlement(matches map[entitlementKey]int, accountID int64, featureID int) error {
	if count := matches[entitlementKey{accountID, featureID}]; count != 1 {
		return fmt.Errorf("entitlement anti-join invariant: account %d feature %d matched %d rows, want exactly 1", accountID, featureID, count)
	}
	return nil
}

func usageAssignment(accountID int64, slot int, entitled []int) (int, int) {
	if accountID == Northstar.AccountID {
		users := [...]int{1, 2, 3, 4, 1}
		features := [...]int{1, 1, 2, 1, 2}
		return users[slot%len(users)], features[slot%len(features)]
	}
	return slot + 1, entitled[slot%len(entitled)]
}

// IDs are injective by construction: account occupies the high decimal
// positions, local user the next positions, and feature (<100) the low two.
func usageUserID(accountID int64, localUser int) int64 {
	return accountID*1_000 + int64(localUser)
}

func usageUserFeatureID(userID int64, featureID int) int64 {
	return userID*100 + int64(featureID)
}

func usageCounts(rng *rand.Rand, accountID int64, slot int, day time.Time) (int, int) {
	if accountID == Northstar.AccountID {
		totals := [...]int{1, 1, 2, 1, 1}
		errors := 0
		if slot == 4 && day.Day()%5 == 0 {
			errors = 1
		}
		return totals[slot%len(totals)], errors
	}
	total := 1 + int(rng.Uint64N(80))
	errors := 0
	if rng.Uint64N(100) < 12 {
		errors = 1 + int(rng.Uint64N(uint64(min(total, 5))))
	}
	return total, errors
}

func inHalfOpenWindow(value, start, end time.Time) bool {
	return !value.Before(start) && value.Before(end)
}
