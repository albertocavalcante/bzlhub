package bzlhub

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/albertocavalcante/assay/report"
)

// TestSyncRunLoop_FirstIterationIsImmediate asserts the daemon
// runs Sync on entry, not after the first Ticker fires. Operators
// running `bzlhub sync run --interval=15m` expect drift to start
// updating immediately, not after waiting the full interval.
func TestSyncRunLoop_FirstIterationIsImmediate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := newTestService(t)
		_, _, mirror := bootstrapMirrorFromRemote(t, map[string]string{
			"foo": `{"versions":["1.0.0"]}`,
		})
		svc.UseMirror(mirror)
		writeServiceReport(t, t.Context(), svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})

		ctx, cancel := context.WithCancel(t.Context())
		var calls atomicInt
		done := make(chan error, 1)
		go func() {
			done <- svc.SyncRunLoop(ctx, SyncRunOptions{}, time.Hour, func(SyncRunReceipt, error) {
				calls.add(1)
			})
		}()

		// Advance just enough for the first iteration to land.
		time.Sleep(time.Second)
		synctest.Wait()
		if got := calls.load(); got < 1 {
			t.Errorf("after 1s, iterations = %d; want >= 1 (first should be immediate)", got)
		}
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("loop returned %v; want nil or context.Canceled", err)
		}
	})
}

// TestSyncRunLoop_FiresEveryInterval pins the cadence: after T =
// 3 × interval, we observe exactly 4 iterations (the initial one
// plus three Ticker fires). synctest's synthetic clock makes this
// flake-free.
func TestSyncRunLoop_FiresEveryInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := newTestService(t)
		_, _, mirror := bootstrapMirrorFromRemote(t, map[string]string{
			"foo": `{"versions":["1.0.0"]}`,
		})
		svc.UseMirror(mirror)

		ctx, cancel := context.WithCancel(t.Context())
		var calls atomicInt
		done := make(chan error, 1)
		go func() {
			done <- svc.SyncRunLoop(ctx, SyncRunOptions{}, 5*time.Minute, func(SyncRunReceipt, error) {
				calls.add(1)
			})
		}()

		// Initial iteration + 3 Ticker fires at t=5, 10, 15.
		time.Sleep(15*time.Minute + time.Second)
		synctest.Wait()

		if got := calls.load(); got != 4 {
			t.Errorf("iterations after 15m = %d; want 4 (initial + 3 ticks)", got)
		}
		cancel()
		<-done
	})
}

// TestSyncRunLoop_CancellationStopsCleanly asserts ctx cancellation
// returns from SyncRunLoop with no goroutine leak. The done channel
// closing within the synctest bubble proves the loop exited.
func TestSyncRunLoop_CancellationStopsCleanly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := newTestService(t)
		_, _, mirror := bootstrapMirrorFromRemote(t, map[string]string{
			"foo": `{"versions":["1.0.0"]}`,
		})
		svc.UseMirror(mirror)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			done <- svc.SyncRunLoop(ctx, SyncRunOptions{}, time.Hour, nil)
		}()

		// Let the first iteration land, then cancel.
		time.Sleep(time.Second)
		synctest.Wait()
		cancel()

		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("loop returned %v; want nil or context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("loop didn't return within 10s of cancellation (synthetic)")
		}
	})
}

// TestSyncRunLoop_PerIterationErrorDoesNotBreakLoop asserts the
// daemon survives a transient sync failure. The callback sees the
// error; the loop keeps going.
func TestSyncRunLoop_PerIterationErrorDoesNotBreakLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := newTestService(t)
		// No Mirror — every iteration's underlying SyncRun returns
		// ErrNoMirrorForDrift. Loop should keep firing.

		ctx, cancel := context.WithCancel(t.Context())
		var errCount, callCount atomicInt
		done := make(chan error, 1)
		go func() {
			done <- svc.SyncRunLoop(ctx, SyncRunOptions{}, time.Minute, func(_ SyncRunReceipt, err error) {
				callCount.add(1)
				if err != nil {
					errCount.add(1)
				}
			})
		}()

		time.Sleep(3*time.Minute + time.Second)
		synctest.Wait()

		if got := callCount.load(); got < 3 {
			t.Errorf("iterations = %d; want >= 3 (loop should survive errors)", got)
		}
		if errCount.load() != callCount.load() {
			t.Errorf("errCount = %d, callCount = %d; every iteration should be an error (no Mirror)",
				errCount.load(), callCount.load())
		}
		cancel()
		<-done
	})
}

// TestSyncRunLoop_ZeroIntervalIsExplicitError asserts the
// explicit-failure contract for the misconfigured case. We don't
// want a zero interval to spin a hot loop.
func TestSyncRunLoop_ZeroIntervalIsExplicitError(t *testing.T) {
	svc := newTestService(t)
	err := svc.SyncRunLoop(t.Context(), SyncRunOptions{}, 0, nil)
	if err == nil {
		t.Errorf("zero interval returned nil; expected an explicit error")
	}
}

// atomicInt is a tiny test helper for race-free counters under
// synctest (where the bubble runs goroutines sequentially but
// race detector still wants the increment guarded).
type atomicInt struct {
	mu sync.Mutex
	n  int
}

func (a *atomicInt) add(n int) {
	a.mu.Lock()
	a.n += n
	a.mu.Unlock()
}

func (a *atomicInt) load() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}
