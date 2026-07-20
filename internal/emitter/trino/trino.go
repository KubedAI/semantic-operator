// Package trino implements the Trino SQL dialect.
package trino

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/KubedAI/semantic-operator/internal/emitter"
)

// Dialect emits Trino (ANSI-family) SQL.
type Dialect struct{}

func init() { emitter.Register(Dialect{}) }

func (Dialect) Name() string { return "trino" }

func (Dialect) QuoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func (d Dialect) QualifyTable(catalog, database, table string) string {
	return d.QuoteIdent(catalog) + "." + d.QuoteIdent(database) + "." + d.QuoteIdent(table)
}

// Literal renders a Go value as a Trino SQL literal. Trino strings are
// standard SQL: single quotes double to escape, and backslash is an ordinary
// character (unlike the MySQL family).
func (Dialect) Literal(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "NULL", nil
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'", nil
	case bool:
		if x {
			return "TRUE", nil
		}
		return "FALSE", nil
	case int:
		return strconv.Itoa(x), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32), nil
	case float64:
		// JSON numbers decode as float64; render integral values as integers.
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10), nil
		}
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	case time.Time:
		return "TIMESTAMP '" + x.UTC().Format("2006-01-02 15:04:05") + "'", nil
	default:
		return "", fmt.Errorf("unsupported literal type %T", v)
	}
}

func (Dialect) DateTrunc(grain, scalar string) string {
	return "DATE_TRUNC('" + grain + "', " + scalar + ")"
}

func (Dialect) NullSafeEq(a, b string) string {
	return a + " IS NOT DISTINCT FROM " + b
}

func (Dialect) CreateSchema(quotedSchema string) string {
	return "CREATE SCHEMA IF NOT EXISTS " + quotedSchema
}
