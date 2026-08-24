package serving

import (
	"errors"
	"strings"
	"testing"

	"github.com/KubedAI/semantic-operator/internal/planner"
)

// A request with no limit used to compile to SQL with no LIMIT clause, so one
// high-cardinality grouping could return the whole table. It now compiles with
// one row past the default. That extra row is the probe Service.Query uses to
// tell a complete result from a larger one it must refuse.
func TestUnsetLimitGetsTheDefaultProbe(t *testing.T) {
	got, err := Limits{}.apply(planner.Request{Metrics: []string{"revenue"}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if want := DefaultLimits().DefaultRowLimit + 1; got.Limit != want {
		t.Fatalf("limit = %d, want the default plus the probe row %d", got.Limit, want)
	}
}

// A zero Limits must mean "the defaults", never "unbounded", so a Service
// built without configuring limits is still safe.
func TestZeroLimitsMeansDefaultsNotUnlimited(t *testing.T) {
	l := Limits{}.withDefaults()
	if l.MaxRowLimit != DefaultLimits().MaxRowLimit || l.MaxResultBytes != DefaultLimits().MaxResultBytes {
		t.Fatalf("zero Limits did not fall back to defaults: %+v", l)
	}
}

// Over the maximum the request is refused, not trimmed. A silently truncated
// result is indistinguishable from a complete one, which is the class of
// confidently wrong answer this project exists to prevent.
func TestExcessiveLimitIsRefusedNotClamped(t *testing.T) {
	_, err := Limits{}.apply(planner.Request{Metrics: []string{"revenue"}, Limit: 999999})
	if !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("want ErrRequestTooLarge, got %v", err)
	}
	if !strings.Contains(err.Error(), "10000") {
		t.Fatalf("error should name the maximum, got %q", err)
	}
}

func TestRequestShapeBounds(t *testing.T) {
	big := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "x"
		}
		return out
	}
	cases := []struct {
		name string
		req  planner.Request
	}{
		{"too many metrics", planner.Request{Metrics: big(26)}},
		{"too many dimensions", planner.Request{Metrics: []string{"m"}, Dimensions: big(26)}},
		{"negative limit", planner.Request{Metrics: []string{"m"}, Limit: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := (Limits{}).apply(tc.req); !errors.Is(err, ErrRequestTooLarge) {
				t.Fatalf("want ErrRequestTooLarge, got %v", err)
			}
		})
	}

	// An oversized IN list is its own bound: it inflates the SQL text rather
	// than the row count, so the row limit does not catch it.
	vals := make([]any, 1001)
	req := planner.Request{Metrics: []string{"m"}, Filters: []planner.Filter{{Field: "store.s_state", Op: "IN", Values: vals}}}
	if _, err := (Limits{}).apply(req); !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("oversized IN list: want ErrRequestTooLarge, got %v", err)
	}
}

// A cache ceiling above the result ceiling can never be reached, so it is
// folded down rather than left as a bound that does nothing.
func TestCacheCeilingNeverExceedsResultCeiling(t *testing.T) {
	l := Limits{MaxResultBytes: 1 << 20, MaxCacheEntryBytes: 64 << 20}.withDefaults()
	if l.MaxCacheEntryBytes != l.MaxResultBytes {
		t.Fatalf("cache ceiling %d should be folded to result ceiling %d", l.MaxCacheEntryBytes, l.MaxResultBytes)
	}
}

// Limits are applied before the plan cache key is computed, so an explicit
// limit and a defaulted one do not share a cache entry.
func TestDefaultedAndExplicitLimitsHashDifferently(t *testing.T) {
	unset, err := Limits{}.apply(planner.Request{Metrics: []string{"revenue"}})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := Limits{}.apply(planner.Request{Metrics: []string{"revenue"}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if planner.RequestHash(unset, "analyst") == planner.RequestHash(explicit, "analyst") {
		t.Fatal("a defaulted limit and an explicit one must not share a plan cache key")
	}
}

// The concurrency bound and the result ceiling form a memory budget together.
// A per-result ceiling means nothing if enough requests may hold one at once,
// which is how the defaults were originally wrong: 32 x 32 MiB is 1 GiB
// against a 512 MiB pod.
func TestDefaultMemoryBudgetFitsTheDefaultPod(t *testing.T) {
	const podLimitBytes = 512 << 20 // matches values.yaml server.resources.limits.memory

	l := DefaultLimits()
	worstCase := l.MaxConcurrentQueries * l.MaxResultBytes

	// Leave at least half the pod for the encoded copy made while serializing,
	// plus the runtime, caches, and everything else.
	if worstCase > podLimitBytes/2 {
		t.Fatalf("worst case %d MiB (%d concurrent x %d MiB) exceeds half of the %d MiB pod limit",
			worstCase>>20, l.MaxConcurrentQueries, l.MaxResultBytes>>20, podLimitBytes>>20)
	}
}
