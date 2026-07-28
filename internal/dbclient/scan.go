package dbclient

import (
	"errors"
	"fmt"
	"time"
)

// ErrResultTooLarge marks a result abandoned partway through because it
// exceeded the configured byte ceiling.
var ErrResultTooLarge = errors.New("result exceeds the configured byte limit")

// DefaultMaxResultBytes is the ceiling applied when a Config leaves it unset,
// so a client can never be constructed with no bound at all.
const DefaultMaxResultBytes = 32 << 20 // 32 MiB

// RowScanner is the part of *sql.Rows this package needs. Narrowing it keeps
// the ceiling testable without standing up a driver.
type RowScanner interface {
	Columns() ([]string, error)
	Next() bool
	Scan(dest ...any) error
	Err() error
}

// ScanRows reads a result set into memory under a byte ceiling.
//
// The ceiling is enforced while scanning, not afterwards. Checking the size of
// a finished result cannot prevent the allocation that produced it: a query
// returning a handful of very large cells exhausts the pod before any check
// runs, and the row limit does not help because the row count is small. The
// only place the growth can actually be stopped is here, between reads.
//
// Sizing measures the encoded width, not the scanned width. JSON escaping can
// multiply a string several times over, so a ceiling applied to raw bytes lets
// through results that blow the budget the moment they are marshalled.
func ScanRows(rows RowScanner, maxBytes int) ([]string, [][]any, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResultBytes
	}
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}

	var out [][]any
	total := 0
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		// The driver hands back []byte backed by a buffer it reuses, so the
		// copy to string is required for correctness, not only for JSON.
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
			total += encodedSize(vals[i]) + 1 // value plus its separator
		}
		total += 2 // row brackets

		if total > maxBytes {
			return nil, nil, fmt.Errorf("%w: stopped after %d rows at roughly %d bytes, the limit is %d; narrow the request or lower the row limit",
				ErrResultTooLarge, len(out)+1, total, maxBytes)
		}
		out = append(out, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if out == nil {
		out = [][]any{}
	}
	return cols, out, nil
}

// encodedSize is the number of bytes a scanned value occupies once JSON
// encoded. Exact for the cases that matter and never an underestimate.
func encodedSize(v any) int {
	switch t := v.(type) {
	case nil:
		return 4 // null
	case string:
		return jsonStringLen(t)
	case []byte:
		return jsonStringLen(string(t))
	case time.Time:
		return len(time.RFC3339Nano) + 2
	case bool:
		return 5
	default:
		// Numeric types render in well under this.
		return 24
	}
}

// jsonStringLen is the encoded width of a string, quotes included.
//
// Counting raw bytes is not good enough. encoding/json escapes control
// characters as six-byte \u00xx sequences, so a column of control characters
// encodes six times larger than it scanned. A ceiling measured on raw length
// would pass a result that then triples or worse during marshalling, which is
// exactly the allocation the ceiling exists to prevent.
//
// It also escapes <, > and & by default for HTML safety, which is another
// six bytes each and easy to forget.
func jsonStringLen(s string) int {
	n := 2 // surrounding quotes
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"', c == '\\':
			n += 2
		case c == '\n', c == '\r', c == '\t':
			n += 2 // short escapes
		case c == '<', c == '>', c == '&':
			n += 6 // \u003c and friends, escaped for HTML safety
		case c < 0x20:
			n += 6 // \u00xx
		default:
			// Multi-byte runes pass through unescaped, and indexing by byte
			// counts each of their bytes exactly once.
			n++
		}
	}
	return n
}
