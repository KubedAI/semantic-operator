package dbclient

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeRows replays a fixed result set through the RowScanner interface.
type fakeRows struct {
	cols []string
	data [][]any
	i    int
}

func (f *fakeRows) Columns() ([]string, error) { return f.cols, nil }
func (f *fakeRows) Next() bool                 { f.i++; return f.i <= len(f.data) }
func (f *fakeRows) Err() error                 { return nil }
func (f *fakeRows) Scan(dest ...any) error {
	for j, d := range dest {
		p, ok := d.(*any)
		if !ok {
			return errors.New("unexpected destination type")
		}
		*p = f.data[f.i-1][j]
	}
	return nil
}

func rowsOf(n int, cell any) *fakeRows {
	data := make([][]any, n)
	for i := range data {
		data[i] = []any{cell}
	}
	return &fakeRows{cols: []string{"c"}, data: data}
}

// A result made of a few very large cells must be abandoned while scanning.
// The row limit cannot catch this shape, and measuring afterwards is too late
// because the memory has already been allocated.
func TestScanRowsStopsOnLargeCellsNotRowCount(t *testing.T) {
	_, _, err := ScanRows(rowsOf(100, strings.Repeat("x", 4096)), 10*1024)
	if !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("want ErrResultTooLarge, got %v", err)
	}
	if !strings.Contains(err.Error(), "stopped after") {
		t.Fatalf("error should say where it stopped, got %v", err)
	}
}

// Scanning must stop early rather than read the whole set and then complain.
func TestScanRowsAbandonsEarly(t *testing.T) {
	r := rowsOf(1000, strings.Repeat("x", 1024))
	if _, _, err := ScanRows(r, 8*1024); !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("want ErrResultTooLarge, got %v", err)
	}
	if r.i > 20 {
		t.Fatalf("read %d rows before stopping, the ceiling should bite far sooner", r.i)
	}
}

func TestScanRowsReturnsSmallResults(t *testing.T) {
	cols, out, err := ScanRows(&fakeRows{
		cols: []string{"a", "b"},
		data: [][]any{{"x", 1}, {"y", 2}},
	}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 || len(out) != 2 {
		t.Fatalf("got %d cols, %d rows", len(cols), len(out))
	}
}

// A zero ceiling must mean the default, never unbounded.
func TestScanRowsZeroCeilingIsTheDefaultNotUnbounded(t *testing.T) {
	if _, _, err := ScanRows(rowsOf(1, "x"), 0); err != nil {
		t.Fatalf("a zero ceiling should fall back to the default: %v", err)
	}
	// And the default must still be a real bound.
	if _, _, err := ScanRows(rowsOf(20000, strings.Repeat("x", 4096)), 0); !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("the default ceiling must still bound the result, got %v", err)
	}
}

// An empty result is a result, not a nil slice the caller has to guard.
func TestScanRowsEmptyResultIsNotNil(t *testing.T) {
	_, out, err := ScanRows(&fakeRows{cols: []string{"a"}}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Fatal("empty result should be an empty slice, not nil")
	}
}

// The ceiling is only meaningful if the estimate matches what json.Marshal
// actually produces. Raw byte length does not: control characters encode six
// times larger, so a result measured raw sails past the ceiling on marshal.
func TestEncodedSizeMatchesRealJSON(t *testing.T) {
	cases := []string{
		"plain",
		"with \"quotes\" and \\ backslash",
		"tab\there\nnewline",
		"\x00\x01\x02 control characters",
		"html <script> & entities",
		"unicode ünïcödé 日本語",
		"",
	}
	for _, s := range cases {
		encoded, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		got := jsonStringLen(s)
		if got != len(encoded) {
			t.Errorf("jsonStringLen(%q) = %d, json.Marshal produced %d bytes (%s)",
				s, got, len(encoded), encoded)
		}
	}
}

// Control characters are the case that breaks a raw-length ceiling. A result
// well under the limit by raw bytes must still be refused once its encoded
// width is counted.
func TestScanRowsCountsEscapeExpansion(t *testing.T) {
	// 1 KiB of control characters encodes to about 6 KiB.
	cell := strings.Repeat("\x01", 1024)
	if jsonStringLen(cell) < 6*1024 {
		t.Fatalf("expected roughly sixfold expansion, got %d", jsonStringLen(cell))
	}

	// Ten rows are ~10 KiB raw and ~60 KiB encoded. A 20 KiB ceiling must
	// refuse them, which a raw-length count would not.
	_, _, err := ScanRows(rowsOf(10, cell), 20*1024)
	if !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("escape expansion was not counted, got %v", err)
	}
	// The same byte count of plain text fits comfortably.
	if _, _, err := ScanRows(rowsOf(10, strings.Repeat("x", 1024)), 20*1024); err != nil {
		t.Fatalf("plain text of the same raw size should fit: %v", err)
	}
}
