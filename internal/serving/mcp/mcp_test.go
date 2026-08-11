package mcp

import (
	"reflect"
	"testing"

	"github.com/KubedAI/semantic-operator/internal/planner"
)

func TestPlannerRequestMapsOrderBy(t *testing.T) {
	in := queryIn{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_category"},
		Filters: []filterIn{{
			Field: "store.s_state", Op: "IN", Values: []any{"NY", "CA"},
		}},
		Grain: "month",
		OrderBy: []orderByIn{
			{Field: "total_sales", Direction: "desc"},
			{Field: "item.i_category", Direction: "asc"},
		},
		Limit: 5,
	}

	got := in.plannerRequest()
	want := planner.Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_category"},
		Filters: []planner.Filter{{
			Field: "store.s_state", Op: "IN", Values: []any{"NY", "CA"},
		}},
		TimeGrain: "month",
		OrderBy: []planner.OrderByClause{
			{Field: "total_sales", Direction: "desc"},
			{Field: "item.i_category", Direction: "asc"},
		},
		Limit: 5,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("planner request mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}
