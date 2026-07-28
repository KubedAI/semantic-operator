package serving

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// blockingHandler stands in for a slow client. It reports when it has been
// entered and does not return until released, which is exactly the window in
// which the result is still allocated.
type blockingHandler struct {
	entered chan struct{}
	release chan struct{}
}

func (h *blockingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.entered <- struct{}{}
	<-h.release
	w.WriteHeader(http.StatusOK)
}

// A slot must be held for the whole handler, not released when the query
// returns. Otherwise a slow client keeps its result in memory while the next
// request takes the freed slot and adds another.
func TestSlotIsHeldUntilTheResponseCompletes(t *testing.T) {
	h := &blockingHandler{entered: make(chan struct{}, 1), release: make(chan struct{})}
	l := NewLimiter(1, 200*time.Millisecond)
	srv := l.Middleware(h)

	// First request enters and stays inside the handler.
	go srv.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/models", nil))
	select {
	case <-h.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first request never entered the handler")
	}
	if l.InFlight() != 1 {
		t.Fatalf("in flight = %d, want 1 while the handler is still running", l.InFlight())
	}

	// Second request must not get in while the first is still writing.
	w2 := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { srv.ServeHTTP(w2, httptest.NewRequest("GET", "/v1/models", nil)); close(done) }()

	select {
	case <-h.entered:
		t.Fatal("second request entered the handler while the first still held the slot")
	case <-time.After(50 * time.Millisecond):
	}

	// It waits, then is rejected rather than queued forever.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second request neither ran nor was rejected")
	}
	if w2.Code != http.StatusServiceUnavailable {
		t.Fatalf("saturated server returned %d, want 503", w2.Code)
	}
	if w2.Header().Get("Retry-After") == "" {
		t.Error("a 503 from the limiter should carry Retry-After")
	}

	// Once the first finishes, the slot comes back.
	close(h.release)
	deadline := time.Now().Add(2 * time.Second)
	for l.InFlight() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if l.InFlight() != 0 {
		t.Fatalf("slot not released after the handler returned, in flight = %d", l.InFlight())
	}
}

// A waiting caller that disconnects must free its wait immediately rather than
// occupy the queue for the full timeout.
func TestWaitingRequestHonoursClientCancellation(t *testing.T) {
	h := &blockingHandler{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(h.release)
	l := NewLimiter(1, 10*time.Second)
	srv := l.Middleware(h)

	go srv.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/models", nil))
	<-h.entered

	req := httptest.NewRequest("GET", "/v1/models", nil)
	ctx, cancel := context.WithCancel(req.Context())
	done := make(chan struct{})
	go func() { srv.ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx)); close(done) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled request kept waiting for a slot")
	}
}

// Concurrency never exceeds the bound under load.
func TestLimiterNeverExceedsItsBound(t *testing.T) {
	const bound = 3
	l := NewLimiter(bound, 5*time.Second)

	var mu sync.Mutex
	cur, peak := 0, 0
	srv := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cur++
		if cur > peak {
			peak = cur
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		cur--
		mu.Unlock()
	}))

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/models", nil))
		}()
	}
	wg.Wait()

	if peak > bound {
		t.Fatalf("peak concurrency %d exceeded the bound of %d", peak, bound)
	}
}
