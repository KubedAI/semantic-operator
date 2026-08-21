// Package gen provides deterministic, streaming synthetic data for the local
// SaaS accounts demo. Each table has an independent random stream, so
// loading or skipping one table cannot change another table's rows.
package gen

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"

	"account-demo.local/datagen/constants"
)

// Row is ordered to match the corresponding TableSchema.Columns slice.
type Row []any

type generator func(*rand.Rand, func(Row) error) error

// tableOrder is the fixed generation/load order. It is shared so neither map
// iteration nor load decisions can change output.
var tableOrder = []string{
	"date_dim",
	"account",
	"account_primary_contact",
	"plan",
	"contract",
	"product_feature",
	"account_feature_entitlement",
	"subscription_monthly",
	"usage_daily",
	"support_ticket",
	"account_feature_monthly",
}

var generators = map[string]generator{
	"date_dim":                    generateDates,
	"account":                     generateAccounts,
	"account_primary_contact":     generateContacts,
	"plan":                        generatePlans,
	"contract":                    generateContracts,
	"product_feature":             generateFeatures,
	"account_feature_entitlement": generateEntitlements,
	"subscription_monthly":        generateSubscriptions,
	"usage_daily":                 generateUsage,
	"support_ticket":              generateSupport,
	"account_feature_monthly":     generateAccountFeatureMonthly,
}

// Tables returns the business tables in their fixed generation order.
func Tables() []string {
	return append([]string(nil), tableOrder...)
}

// Rows generates table rows and delivers at most batchSize rows per callback.
// A callback must consume a batch before returning; its backing storage is reused.
func Rows(table string, batchSize int, fn func([]Row) error) error {
	if batchSize <= 0 {
		return fmt.Errorf("batch size must be positive: %d", batchSize)
	}
	if fn == nil {
		return errors.New("row callback must not be nil")
	}
	generate, ok := generators[table]
	if !ok {
		return fmt.Errorf("unknown table %q", table)
	}

	batch := make([]Row, 0, batchSize)
	emit := func(row Row) error {
		batch = append(batch, row)
		if len(batch) < batchSize {
			return nil
		}
		if err := fn(batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	if err := generate(tableRand(table), emit); err != nil {
		return fmt.Errorf("generate %s: %w", table, err)
	}
	if len(batch) > 0 {
		if err := fn(batch); err != nil {
			return err
		}
	}
	return nil
}

func tableRand(table string) *rand.Rand {
	seedInput := fmt.Sprintf("%d\x00%s", constants.Seed, table)
	digest := sha256.Sum256([]byte(seedInput))
	return rand.New(rand.NewPCG(
		binary.LittleEndian.Uint64(digest[:8]),
		binary.LittleEndian.Uint64(digest[8:16]),
	))
}
