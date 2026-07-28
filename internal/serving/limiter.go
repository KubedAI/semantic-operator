package serving

import (
	"net/http"
	"strconv"
	"time"
)

// Limiter bounds how many requests may be in flight at once.
//
// It wraps the whole handler, not just query execution. A slot released when
// the query returns is released too early: the result is still allocated while
// the response is serialized and written, and a slow client holds that memory
// for as long as it takes to read. The next request would take the freed slot
// and add its own result on top, so the bound would not bound anything.
//
// Wrapping the handler also covers cache hits, which never touch the engine
// and can still allocate a full cached result per concurrent request.
type Limiter struct {
	slots chan struct{}
	wait  time.Duration
}

// NewLimiter bounds concurrency to n requests. A caller that cannot get a slot
// within wait is rejected with 503 rather than queued indefinitely, because an
// unbounded queue converts a load problem into a timeout problem further out.
func NewLimiter(n int, wait time.Duration) *Limiter {
	if n <= 0 {
		n = DefaultLimits().MaxConcurrentQueries
	}
	if wait <= 0 {
		wait = 5 * time.Second
	}
	return &Limiter{slots: make(chan struct{}, n), wait: wait}
}

// Middleware holds a slot for the entire handler, releasing it only once the
// response has been written.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := time.NewTimer(l.wait)
		defer timer.Stop()

		select {
		case l.slots <- struct{}{}:
			defer func() { <-l.slots }()
		case <-r.Context().Done():
			// The caller gave up first. Nothing useful to write.
			return
		case <-timer.C:
			w.Header().Set("Retry-After", strconv.Itoa(int(l.wait.Seconds())+1))
			http.Error(w, `{"error":"server is at its concurrency limit, retry shortly"}`,
				http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// InFlight is the number of slots currently held, for tests and metrics.
func (l *Limiter) InFlight() int { return len(l.slots) }
