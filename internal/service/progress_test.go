package service

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nvrmndmnm/blingtronsfavor/internal/github"
)

// TestProgressSinkSerializesConcurrentEmit pins the delivery contract
// (risk R3): parallel workers emit through one sink and the callback is
// never invoked concurrently, so it need not be goroutine-safe. Run with
// -race this also proves the sink itself is race-free.
func TestProgressSinkSerializesConcurrentEmit(t *testing.T) {
	var active atomic.Bool
	var overlaps atomic.Int64
	var delivered int
	sink := newProgressSink(func(ProgressEvent) {
		if !active.CompareAndSwap(false, true) {
			overlaps.Add(1)
		}
		delivered++
		active.Store(false)
	})
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 1; i <= 50; i++ {
				sink.emit(ProgressEvent{Index: i})
			}
		}()
	}
	wg.Wait()
	if n := overlaps.Load(); n != 0 {
		t.Errorf("callback ran concurrently %d time(s)", n)
	}
	if delivered != 400 {
		t.Errorf("delivered %d events, want 400", delivered)
	}
}

// TestProgressSinkNoopWithoutCallback pins the nil contract: flows never
// special-case "no reporting" and nothing panics without a listener.
func TestProgressSinkNoopWithoutCallback(t *testing.T) {
	newProgressSink(nil).emit(ProgressEvent{})
	var missing *progressSink
	missing.emit(ProgressEvent{})
	progressSlot{}.emit(PhaseResolving, Detail{})
	if (progressSlot{}).rateLimited(PhaseResolving, errors.New("plain")) {
		t.Error("rateLimited reported a plain error as a rate limit")
	}
}

// TestProgressSlotRateLimited pins the rate-limit reporting (risk R6): a
// GitHub throttle produces an explicit event carrying the wait guidance in
// the unit's current phase, and other errors stay silent.
func TestProgressSlotRateLimited(t *testing.T) {
	var got []ProgressEvent
	slot := progressSlot{
		p:     newProgressSink(func(ev ProgressEvent) { got = append(got, ev) }),
		unit:  Unit{AddonID: "Demo"},
		index: 2,
		total: 5,
	}
	err := &github.RateLimitError{RetryAfter: 30 * time.Second}
	if !slot.rateLimited(PhaseResolving, err) {
		t.Fatal("rate limit not reported")
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	ev := got[0]
	if ev.Phase != PhaseResolving || ev.Unit != slot.unit || ev.Index != 2 || ev.Total != 5 {
		t.Errorf("event = %+v, want the unit's frame with phase resolving", ev)
	}
	if ev.Detail.Err != error(err) {
		t.Errorf("detail error = %v, want the rate-limit error", ev.Detail.Err)
	}
	if !strings.Contains(ev.Detail.Reason, "rate limited, retry after") {
		t.Errorf("reason = %q, want the wait guidance", ev.Detail.Reason)
	}
}
