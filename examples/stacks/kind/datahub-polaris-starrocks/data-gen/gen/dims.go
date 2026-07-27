package gen

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"chd.local/datagen/constants"
)

const firstAccountID int64 = 1001

var (
	segments          = []string{"Enterprise", "Mid-Market", "SMB"}
	regions           = []string{"NA", "EMEA", "APAC", "LATAM"}
	industries        = []string{"Technology", "Financial Services", "Healthcare", "Retail", "Manufacturing", "Media", "Education", "Professional Services"}
	csmTeams          = []string{"Enterprise North America", "Enterprise International", "Growth North America", "Growth International", "Digital Success"}
	lifecycleStatuses = []string{"trial", "active", "paused", "churned", "renewal-due"}
	companyPrefixes   = []string{"Acme", "Aperture", "Atlas", "Bluebird", "Cedar", "Evergreen", "Harbor", "Juniper", "Keystone", "Lighthouse", "Meridian", "Nimbus", "Orchard", "Pioneer", "Redwood", "Summit", "Vertex", "Willow"}
	companySuffixes   = []string{"Analytics", "Cloud", "Dynamics", "Group", "Industries", "Labs", "Logistics", "Networks", "Partners", "Services", "Software", "Works"}
	firstNames        = []string{"Amara", "Diego", "Elena", "Hana", "Ibrahim", "Jordan", "Kai", "Leila", "Mateo", "Nia", "Priya", "Ravi", "Sofia", "Tariq", "Yuki", "Zoe"}
	lastNames         = []string{"Bennett", "Chen", "Diaz", "Fischer", "Gupta", "Haddad", "Ito", "Johnson", "Kim", "Lopez", "Mensah", "Novak", "Patel", "Rossi", "Singh", "Williams"}
	jobTitles         = []string{"Chief Information Officer", "Director of Operations", "Head of Data", "IT Manager", "Revenue Operations Lead", "VP, Customer Experience", "VP, Engineering"}
)

func generateDates(_ *rand.Rand, emit func(Row) error) error {
	for day := constants.DateRangeStart; !day.After(constants.DateRangeEnd); day = day.AddDate(0, 0, 1) {
		if err := emit(Row{dateSK(day), day, day.Year(), (int(day.Month())-1)/3 + 1, int(day.Month())}); err != nil {
			return err
		}
	}
	return nil
}

func generateAccounts(rng *rand.Rand, emit func(Row) error) error {
	count := constants.ExpectedTableRowCounts["account"]
	span := int(constants.DateRangeEnd.Sub(constants.AsOfDate).Hours() / 24)
	for i := 0; i < count; i++ {
		id := firstAccountID + int64(i)
		if id == Northstar.AccountID {
			if err := emit(accountAnchorRow(Northstar)); err != nil {
				return err
			}
			continue
		}
		segment := segments[weightedIndex(rng, []uint64{18, 32, 50})]
		region := regions[weightedIndex(rng, []uint64{48, 27, 18, 7})]
		status := lifecycleStatuses[weightedIndex(rng, []uint64{8, 62, 8, 10, 12})]
		team := csmTeams[4]
		if segment == "Enterprise" {
			if region == "NA" {
				team = csmTeams[0]
			} else {
				team = csmTeams[1]
			}
		} else if segment == "Mid-Market" {
			if region == "NA" {
				team = csmTeams[2]
			} else {
				team = csmTeams[3]
			}
		}
		renewal := constants.AsOfDate.AddDate(0, 0, int(rng.Uint64N(uint64(span+365)))-180)
		if status == "renewal-due" {
			days := int(constants.RenewalHorizonEnd.Sub(constants.RenewalHorizonStart).Hours() / 24)
			renewal = constants.RenewalHorizonStart.AddDate(0, 0, int(rng.Uint64N(uint64(days))))
		}
		name := fmt.Sprintf("%s %s %04d", companyPrefixes[rng.Uint64N(uint64(len(companyPrefixes)))], companySuffixes[rng.Uint64N(uint64(len(companySuffixes)))], id)
		if err := emit(Row{id, name, segment, region, industries[rng.Uint64N(uint64(len(industries)))], team, renewal, status}); err != nil {
			return err
		}
	}
	return nil
}

func generateContacts(rng *rand.Rand, emit func(Row) error) error {
	count := constants.ExpectedTableRowCounts["account_primary_contact"]
	for i := 0; i < count; i++ {
		id := firstAccountID + int64(i)
		if id == NorthstarContact.AccountID {
			if err := emit(Row{id, NorthstarContact.FullName, NorthstarContact.Email, NorthstarContact.Phone, NorthstarContact.JobTitle}); err != nil {
				return err
			}
			continue
		}
		first := firstNames[rng.Uint64N(uint64(len(firstNames)))]
		last := lastNames[rng.Uint64N(uint64(len(lastNames)))]
		email := fmt.Sprintf("%s.%s.%d@customer.example", strings.ToLower(first), strings.ToLower(last), id)
		phone := fmt.Sprintf("+1-555-%03d-%04d", (id/10)%1000, id%10000)
		if err := emit(Row{id, first + " " + last, email, phone, jobTitles[rng.Uint64N(uint64(len(jobTitles)))]}); err != nil {
			return err
		}
	}
	return nil
}

func generatePlans(_ *rand.Rand, emit func(Row) error) error {
	families := []string{"Engage", "Insights", "Platform"}
	tiers := []string{"Starter", "Growth", "Business", "Enterprise"}
	id := 1
	for _, family := range families {
		for _, tier := range tiers {
			if err := emit(Row{id, family + " " + tier, tier, family}); err != nil {
				return err
			}
			id++
		}
	}
	return nil
}

func generateContracts(rng *rand.Rand, emit func(Row) error) error {
	count := constants.ExpectedTableRowCounts["contract"]
	accountCount := constants.ExpectedTableRowCounts["account"]
	for i := 0; i < count; i++ {
		contractID := int64(50_001 + i)
		accountID := firstAccountID + int64(i%accountCount)
		start := utcDate(2024, time.January, 1).AddDate(0, 0, int(rng.Uint64N(730)))
		termMonths := []int{12, 12, 12, 24, 36}[rng.Uint64N(5)]
		end := start.AddDate(0, termMonths, -1)
		if accountID == Northstar.AccountID && i == 0 {
			end = Northstar.RenewalDate
			start = end.AddDate(-1, 0, 1)
		}
		discount := roundMoney(float64(rng.Uint64N(3001)) / 100)
		annualRate := roundMoney(12_000 + float64(rng.Uint64N(988_001)))
		contractValue := roundMoney(annualRate * float64(termMonths) / 12 * (1 - discount/100))
		if err := emit(Row{contractID, accountID, start, end, discount, annualRate, contractValue}); err != nil {
			return err
		}
	}
	return nil
}

func generateFeatures(_ *rand.Rand, emit func(Row) error) error {
	areas := []string{"Analytics", "Automation", "Collaboration", "Data Platform", "Governance", "Integrations", "Security", "Workflow"}
	capabilities := []string{"Core", "Dashboards", "Exports", "Monitoring", "Orchestration", "Studio"}
	criticalities := []string{"low", "medium", "high", "critical"}
	id := 1
	for _, area := range areas {
		for j, capability := range capabilities {
			criticality := criticalities[(id+j)%len(criticalities)]
			if err := emit(Row{id, area + " " + capability, area, criticality}); err != nil {
				return err
			}
			id++
		}
	}
	return nil
}

func generateEntitlements(rng *rand.Rand, emit func(Row) error) error {
	accountCount := constants.ExpectedTableRowCounts["account"]
	featureCount := constants.ExpectedTableRowCounts["product_feature"]
	perAccount := constants.ExpectedTableRowCounts["account_feature_entitlement"] / accountCount
	anchorByFeature := make(map[int]EntitlementAnchor, len(NorthstarEntitlements))
	for _, entitlement := range NorthstarEntitlements {
		anchorByFeature[entitlement.FeatureID] = entitlement
	}
	for i := 0; i < accountCount; i++ {
		accountID := firstAccountID + int64(i)
		for j := 0; j < perAccount; j++ {
			featureID := (i*perAccount+j)%featureCount + 1
			if accountID == Northstar.AccountID {
				anchor, ok := anchorByFeature[featureID]
				if !ok {
					return fmt.Errorf("Northstar entitlement missing for feature %d", featureID)
				}
				if err := emit(entitlementAnchorRow(anchor)); err != nil {
					return err
				}
				continue
			}
			licensed := 10 + int(rng.Uint64N(991))
			eligible := 1 + int(rng.Uint64N(uint64(licensed)))
			adopted := int(rng.Uint64N(uint64(eligible + 1)))
			if err := emit(Row{accountID, featureID, licensed, eligible, adopted}); err != nil {
				return err
			}
		}
	}
	return nil
}

func accountAnchorRow(a AccountAnchor) Row {
	return Row{a.AccountID, a.AccountName, a.Segment, a.Region, a.Industry, a.CSMTeam, a.RenewalDate, a.LifecycleStatus}
}

func entitlementAnchorRow(e EntitlementAnchor) Row {
	return Row{e.AccountID, e.FeatureID, e.LicensedSeats, e.EligibleSeats, e.AdoptedSeats}
}

func dateSK(day time.Time) int { return day.Year()*10_000 + int(day.Month())*100 + day.Day() }

func weightedIndex(rng *rand.Rand, weights []uint64) int {
	var total uint64
	for _, weight := range weights {
		total += weight
	}
	value := rng.Uint64N(total)
	for i, weight := range weights {
		if value < weight {
			return i
		}
		value -= weight
	}
	panic("unreachable weighted index")
}

func roundMoney(value float64) float64 { return math.Round(value*100) / 100 }
