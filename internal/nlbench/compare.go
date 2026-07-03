package nlbench

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Verdict classifies one path's answer against ground truth.
type Verdict string

const (
	VerdictCorrect Verdict = "correct"
	VerdictWrong   Verdict = "wrong"   // executed but numbers do not match
	VerdictFailed  Verdict = "failed"  // SQL errored or referenced nonexistent objects
	VerdictNoQuery Verdict = "noquery" // no SQL produced
)

// RelTolerance is the numeric match tolerance. Ground truth and answers both
// come from StarRocks, so this only absorbs decimal rendering differences.
const RelTolerance = 0.005

// Compare classifies a path result against ground-truth columns/rows.
// Matching is structural, not positional: rows are keyed by their non-numeric
// values (dimension labels) and numeric cells must match within tolerance.
// Column names do not matter; column count beyond ground truth does not
// matter as long as every ground-truth numeric appears in the row.
func Compare(gtCols []string, gtRows [][]any, res PathResult) Verdict {
	if res.Err != "" {
		if res.SQL == "" {
			return VerdictNoQuery
		}
		return VerdictFailed
	}
	if res.SQL == "" {
		return VerdictNoQuery
	}
	gt := normalizeRows(gtRows)
	got := normalizeRows(res.Rows)
	if len(gt) != len(got) {
		return VerdictWrong
	}
	for i := range gt {
		if !rowsMatch(gt[i], got[i]) {
			return VerdictWrong
		}
	}
	return VerdictCorrect
}

// normalizedRow separates label cells from numeric cells.
type normalizedRow struct {
	labels []string
	nums   []float64
}

func normalizeRows(rows [][]any) []normalizedRow {
	out := make([]normalizedRow, 0, len(rows))
	for _, r := range rows {
		var n normalizedRow
		for _, c := range r {
			if f, ok := asNumber(c); ok {
				n.nums = append(n.nums, f)
			} else {
				n.labels = append(n.labels, strings.TrimSpace(fmt.Sprint(c)))
			}
		}
		sort.Strings(n.labels)
		sort.Float64s(n.nums)
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if la, lb := strings.Join(a.labels, "\x00"), strings.Join(b.labels, "\x00"); la != lb {
			return la < lb
		}
		return fmt.Sprint(a.nums) < fmt.Sprint(b.nums)
	})
	return out
}

// rowsMatch requires identical labels and that every ground-truth numeric
// has a within-tolerance counterpart (the answer may carry extra columns).
func rowsMatch(gt, got normalizedRow) bool {
	if strings.Join(gt.labels, "\x00") != strings.Join(got.labels, "\x00") {
		return false
	}
	if len(got.nums) < len(gt.nums) {
		return false
	}
	used := make([]bool, len(got.nums))
	for _, want := range gt.nums {
		found := false
		for i, have := range got.nums {
			if used[i] {
				continue
			}
			if numbersClose(want, have) {
				used[i] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func numbersClose(a, b float64) bool {
	if a == b {
		return true
	}
	den := math.Max(math.Abs(a), math.Abs(b))
	if den == 0 {
		return true
	}
	return math.Abs(a-b)/den <= RelTolerance
}

func asNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return f, true
		}
	case []byte:
		if f, err := strconv.ParseFloat(strings.TrimSpace(string(x)), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// IsHallucination reports whether a failed raw-path attempt referenced
// schema objects that do not exist (as opposed to syntax or type errors):
// the classic confident-but-wrong failure.
func IsHallucination(errMsg string) bool {
	m := strings.ToLower(errMsg)
	return strings.Contains(m, "unknown table") ||
		strings.Contains(m, "unknown column") ||
		strings.Contains(m, "unknown database") ||
		strings.Contains(m, "cannot be resolved") ||
		strings.Contains(m, "does not exist") ||
		strings.Contains(m, "column cannot be found")
}
