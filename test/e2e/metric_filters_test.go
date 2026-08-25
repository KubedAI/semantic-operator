//go:build e2e

package e2e

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const metricFilterE2ELimit = 10000

// Metric filters execute against both engine profiles. Thresholds come from a
// live unfiltered query, so the assertions do not depend on engine-specific
// fixture totals or floating-point aggregation order.
func TestMetricFilters(t *testing.T) {
	tok := token(t, "alice")
	headers := bearer(tok)

	t.Run("flat aggregate", func(t *testing.T) {
		testMetricFilterExecution(t, headers, cfg.metric, false)
	})

	t.Run("split ratio", func(t *testing.T) {
		testMetricFilterExecution(t, headers, cfg.ratioMetric, true)
	})

	t.Run("raw expression rejected", func(t *testing.T) {
		body := metricQuery(cfg.metric, metricFilter{
			metric: "SUM(" + cfg.metric + ")",
			op:     ">",
			value:  0,
		}, 1)
		r, err := queryModel(testCtx(t), cfg.staticNS, cfg.model, headers, body)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		if r.status != http.StatusBadRequest {
			t.Fatalf("raw metric expression: want 400, got %d (%s)", r.status, r.raw)
		}
		if !strings.Contains(r.errMsg, "requested certified metric") {
			t.Fatalf("raw metric expression: unexpected error %q", r.errMsg)
		}
	})
}

type metricFilter struct {
	metric string
	op     string
	value  any
}

func testMetricFilterExecution(t *testing.T, headers map[string]string, metric string, split bool) {
	t.Helper()

	baseline, err := queryModel(testCtx(t), cfg.staticNS, cfg.model, headers,
		metricQuery(metric, metricFilter{}, metricFilterE2ELimit))
	if err != nil {
		t.Fatalf("baseline request: %v", err)
	}
	if baseline.status != http.StatusOK {
		t.Fatalf("baseline %q: want 200, got %d (%s)", metric, baseline.status, baseline.raw)
	}
	if len(baseline.columns) != 2 || baseline.columns[0] != cfg.allowDim || baseline.columns[1] != metric {
		t.Fatalf("baseline %q: unexpected columns %v (%s)", metric, baseline.columns, baseline.raw)
	}
	if len(baseline.rows) < 2 {
		t.Fatalf("baseline %q: need at least two groups, got %d (%s)", metric, len(baseline.rows), baseline.raw)
	}

	threshold := separatingThreshold(t, baseline.rows)
	expected := rowsAbove(t, baseline.rows, threshold)
	if len(expected) == 0 || len(expected) == len(baseline.rows) {
		t.Fatalf("threshold %v did not select a strict subset of %d rows", threshold, len(baseline.rows))
	}

	filter := metricFilter{metric: metric, op: ">", value: threshold}
	filtered, err := queryModel(testCtx(t), cfg.staticNS, cfg.model, headers,
		metricQuery(metric, filter, metricFilterE2ELimit))
	if err != nil {
		t.Fatalf("filtered request: %v", err)
	}
	if filtered.status != http.StatusOK {
		t.Fatalf("filtered %q: want 200, got %d (%s)", metric, filtered.status, filtered.raw)
	}
	if !rowsClose(expected, filtered.rows, 1e-9) {
		t.Fatalf("filtered %q rows %v, want %v (threshold %v, response %s)",
			metric, filtered.rows, expected, threshold, filtered.raw)
	}
	assertMetricFilterSQL(t, filtered.sql, split)

	limited, err := queryModel(testCtx(t), cfg.staticNS, cfg.model, headers,
		metricQuery(metric, filter, 1))
	if err != nil {
		t.Fatalf("limited request: %v", err)
	}
	if limited.status != http.StatusOK {
		t.Fatalf("limited %q: want 200, got %d (%s)", metric, limited.status, limited.raw)
	}
	if len(limited.rows) != 1 || !rowsClose(expected[:1], limited.rows, 1e-9) {
		t.Fatalf("limited %q rows %v, want first filtered row %v (%s)", metric, limited.rows, expected[:1], limited.raw)
	}
}

func metricQuery(metric string, filter metricFilter, limit int) map[string]any {
	q := map[string]any{
		"metrics":    []string{metric},
		"dimensions": []string{cfg.allowDim},
		"orderBy": []map[string]any{
			{"field": metric, "direction": "desc"},
			{"field": cfg.allowDim, "direction": "asc"},
		},
		"limit": limit,
	}
	if filter.metric != "" {
		q["metricFilters"] = []map[string]any{{
			"metric": filter.metric,
			"op":     filter.op,
			"value":  filter.value,
		}}
	}
	return q
}

// separatingThreshold chooses the midpoint of the largest gap between metric
// values. This gives the filtered query the largest margin against last-bit
// differences in parallel floating-point aggregation.
func separatingThreshold(t *testing.T, rows [][]any) float64 {
	t.Helper()
	values := make([]float64, len(rows))
	for i, row := range rows {
		values[i] = rowMetric(t, row)
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(values)))

	gapIndex := -1
	largestGap := 0.0
	for i := 0; i < len(values)-1; i++ {
		if gap := values[i] - values[i+1]; gap > largestGap {
			largestGap = gap
			gapIndex = i
		}
	}
	if gapIndex < 0 {
		t.Fatalf("metric fixture has no distinct grouped values: %v", values)
	}
	return values[gapIndex+1] + largestGap/2
}

func rowsAbove(t *testing.T, rows [][]any, threshold float64) [][]any {
	t.Helper()
	var out [][]any
	for _, row := range rows {
		if rowMetric(t, row) > threshold {
			out = append(out, row)
		}
	}
	return out
}

func rowMetric(t *testing.T, row []any) float64 {
	t.Helper()
	if len(row) != 2 {
		t.Fatalf("metric query returned a row with %d cells: %v", len(row), row)
	}
	switch value := row[1].(type) {
	case float64:
		return value
	case string:
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			t.Fatalf("metric value %q is not numeric: %v", value, err)
		}
		return n
	default:
		t.Fatalf("metric value has unsupported type %T: %v", value, value)
		return 0
	}
}

func assertMetricFilterSQL(t *testing.T, sql string, split bool) {
	t.Helper()
	if sql == "" {
		t.Fatal("query response did not include SQL")
	}
	order := strings.LastIndex(sql, "\nORDER BY ")
	limit := strings.LastIndex(sql, "\nLIMIT ")
	if order < 0 || limit < order {
		t.Fatalf("ORDER BY and LIMIT must follow metric filtering:\n%s", sql)
	}

	if split {
		if !strings.Contains(sql, "\nWITH ") {
			t.Fatalf("split-ratio query is missing CTEs:\n%s", sql)
		}
		if strings.Contains(sql, "\nHAVING ") {
			t.Fatalf("split-ratio metric filter was pushed into an aggregate CTE:\n%s", sql)
		}
		where := strings.LastIndex(sql, "\nWHERE ")
		if where < 0 || where > order {
			t.Fatalf("split-ratio metric filter must be a final WHERE before ORDER BY:\n%s", sql)
		}
		return
	}

	group := strings.LastIndex(sql, "\nGROUP BY ")
	having := strings.LastIndex(sql, "\nHAVING ")
	if group < 0 || having < group || having > order {
		t.Fatalf("flat metric filter must use HAVING after GROUP BY and before ORDER BY:\n%s", sql)
	}
}
