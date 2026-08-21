package gen

import (
	"fmt"
	"math/rand/v2"

	"account-demo.local/datagen/constants"
)

func generateAccountFeatureMonthly(rng *rand.Rand, emit func(Row) error) error {
	months := 4
	entitlementCount := constants.ExpectedTableRowCounts["account_feature_entitlement"]
	if entitlementCount*months != constants.ExpectedTableRowCounts["account_feature_monthly"] {
		return fmt.Errorf("monthly row manifest mismatch: entitlements=%d months=%d rows=%d", entitlementCount, months, constants.ExpectedTableRowCounts["account_feature_monthly"])
	}
	monthStart := constants.CurrentSnapshot.AddDate(0, -(months - 1), 0)
	return generateEntitlements(tableRand("account_feature_entitlement"), func(row Row) error {
		accountID := row[0].(int64)
		featureID := row[1].(int)
		licensed := row[2].(int)
		eligible := row[3].(int)
		currentAdopted := row[4].(int)
		for monthIndex := 0; monthIndex < months; monthIndex++ {
			adopted, active, events := monthlyAdoption(rng, accountID, featureID, monthIndex, currentAdopted)
			if adopted < 0 || adopted > eligible || active < 0 || active > adopted || events < int64(active) {
				return fmt.Errorf("invalid monthly adoption for account %d feature %d: eligible=%d adopted=%d active=%d events=%d", accountID, featureID, eligible, adopted, active, events)
			}
			snapshot := monthStart.AddDate(0, monthIndex, 0)
			if err := emit(Row{accountID, featureID, dateSK(snapshot), licensed, eligible, adopted, active, events}); err != nil {
				return err
			}
		}
		return nil
	})
}

func monthlyAdoption(rng *rand.Rand, accountID int64, featureID, monthIndex, currentAdopted int) (int, int, int64) {
	if accountID == Northstar.AccountID {
		adopted := currentAdopted * (85 + monthIndex*5) / 100
		if monthIndex == 3 {
			adopted = currentAdopted
		}
		active, events := 0, int64(0)
		switch featureID {
		case 1:
			active = min(adopted, monthIndex+1)
			events = int64(90 + monthIndex*10)
		case 2:
			active = min(adopted, 1+monthIndex/2)
			events = int64(30 + monthIndex*10)
		}
		return adopted, active, events
	}

	variation := 80 + monthIndex*5 + int(rng.Uint64N(6))
	adopted := currentAdopted * variation / 100
	if monthIndex == 3 {
		adopted = currentAdopted
	}
	if currentAdopted > 0 && adopted == 0 {
		adopted = 1
	}
	active := 0
	if adopted > 0 {
		active = 1 + int(rng.Uint64N(uint64(adopted)))
	}
	events := int64(active) * int64(1+rng.Uint64N(121))
	return adopted, active, events
}
