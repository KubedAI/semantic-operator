package gen

import (
	"fmt"
	"math/rand/v2"
	"strings"

	"chd.local/datagen/constants"
)

const firstSupportTicketID int64 = 2_000_001

var supportSubjects = []string{
	"Dashboard access issue", "Automation run failed", "Connector needs attention",
	"Export delayed", "Permission review", "Workflow configuration question",
}

func generateSupport(rng *rand.Rand, emit func(Row) error) error {
	count := constants.ExpectedTableRowCounts["support_ticket"]
	observedDays := int(constants.SupportObservedThrough.Sub(constants.DateRangeStart).Hours()/24) + 1
	currentDays := int(constants.SupportObservedThrough.Sub(constants.SupportWindowStart).Hours()/24) + 1
	if observedDays <= 0 || currentDays <= 0 {
		return fmt.Errorf("invalid support date manifest: observed days=%d current days=%d", observedDays, currentDays)
	}

	for i := 0; i < count; i++ {
		ticketID := firstSupportTicketID + int64(i)
		accountID := firstAccountID + 1 + int64((i-NorthstarSupport.TotalTickets+constants.ExpectedTableRowCounts["account"]-1)%(constants.ExpectedTableRowCounts["account"]-1))
		created := constants.DateRangeStart.AddDate(0, 0, i%observedDays)
		featureID := any((i % constants.ExpectedTableRowCounts["product_feature"]) + 1)
		requester := fmt.Sprintf("requester.%d@customer.example", accountID)
		subject := supportSubjects[rng.Uint64N(uint64(len(supportSubjects)))]
		open := i%9 == 0
		escalated := i%17 == 0
		slaMet := i%7 != 0

		if i < NorthstarSupport.TotalTickets {
			accountID = Northstar.AccountID
			created = constants.SupportWindowStart.AddDate(0, 0, i%currentDays)
			if i%3 == 0 {
				featureID = nil
			} else {
				featureID = i%2 + 1
			}
			requester = NorthstarContact.Email
			subject = supportSubjects[i%len(supportSubjects)]
			open = i%5 == 0
			escalated = i%4 == 0
			slaMet = i%3 != 0
		} else if i%5 == 0 {
			featureID = nil
		}

		status := "resolved"
		if open {
			status = "open"
		}
		firstResponse := supportFirstResponseHours(rng, slaMet)
		var resolution any
		if !open {
			resolution = roundMoney(firstResponse + 2 + float64(rng.Uint64N(2399))/10)
		}

		var currentTicketID, currentFirstResponse, currentResolution any
		currentOpen, currentEscalated, currentSLAMet := 0, 0, 0
		if inHalfOpenWindow(created, constants.SupportWindowStart, constants.SupportWindowEnd) {
			currentTicketID = ticketID
			currentOpen = boolInt(open)
			currentEscalated = boolInt(escalated)
			currentSLAMet = boolInt(slaMet)
			currentFirstResponse = firstResponse
			currentResolution = resolution
		}

		if strings.TrimSpace(subject) == "" {
			return fmt.Errorf("support ticket %d has an empty subject", ticketID)
		}
		if err := emit(Row{
			ticketID, accountID, featureID, dateSK(created), requester, subject, status,
			boolInt(escalated), boolInt(slaMet), firstResponse, resolution,
			currentTicketID, currentOpen, currentEscalated, currentSLAMet,
			currentFirstResponse, currentResolution,
		}); err != nil {
			return err
		}
	}
	return nil
}

func supportFirstResponseHours(rng *rand.Rand, slaMet bool) float64 {
	if slaMet {
		return roundMoney(0.25 + float64(rng.Uint64N(350))/100)
	}
	return roundMoney(4.25 + float64(rng.Uint64N(1576))/100)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
