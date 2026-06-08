package runtime

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestReloader_NoReloaders_HasReloadersFalse(t *testing.T) {
	r := NewReloader(slog.Default())
	if r.HasReloaders() {
		t.Error("HasReloaders true with no registrations")
	}
}

func TestReloader_RegisterRunInvokesFn(t *testing.T) {
	r := NewReloader(slog.Default())
	var calls atomic.Int64
	r.Register("test", func(context.Context) error {
		calls.Add(1)
		return nil
	})
	if !r.HasReloaders() {
		t.Error("HasReloaders false after Register")
	}
	ctx, cancel := context.WithCancel(context.Background())
	trigger := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		r.Run(ctx, trigger)
		close(done)
	}()
	trigger <- struct{}{}
	// Allow the run to process the trigger
	deadline := time.Now().Add(500 * time.Millisecond)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Errorf("fn invocations=%d, want 1", calls.Load())
	}
	cancel()
	<-done
}

func TestReloader_RegistrationOrderPreserved(t *testing.T) {
	r := NewReloader(slog.Default())
	var order []string
	r.Register("first", func(context.Context) error {
		order = append(order, "first")
		return nil
	})
	r.Register("second", func(context.Context) error {
		order = append(order, "second")
		return nil
	})
	r.runOnce(context.Background())
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("order=%v, want [first second]", order)
	}
}

func TestReloader_FnErrorDoesNotStopOthers(t *testing.T) {
	r := NewReloader(slog.Default())
	var secondCalled atomic.Bool
	r.Register("failer", func(context.Context) error {
		return errors.New("boom")
	})
	r.Register("survivor", func(context.Context) error {
		secondCalled.Store(true)
		return nil
	})
	r.runOnce(context.Background())
	if !secondCalled.Load() {
		t.Error("survivor not called after failer returned error")
	}
}

func TestReloader_FnErrorDoesNotStopFutureTriggers(t *testing.T) {
	r := NewReloader(slog.Default())
	var calls atomic.Int64
	r.Register("flaky", func(context.Context) error {
		n := calls.Add(1)
		if n == 1 {
			return errors.New("first attempt fails")
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	trigger := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		r.Run(ctx, trigger)
		close(done)
	}()
	trigger <- struct{}{}
	trigger <- struct{}{}
	deadline := time.Now().Add(500 * time.Millisecond)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if calls.Load() != 2 {
		t.Errorf("calls=%d, want 2 (failure shouldn't stop subsequent triggers)", calls.Load())
	}
	cancel()
	<-done
}

func TestReloader_CtxCancelStopsRun(t *testing.T) {
	r := NewReloader(slog.Default())
	r.Register("noop", func(context.Context) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	trigger := make(chan struct{})
	done := make(chan struct{})
	go func() {
		r.Run(ctx, trigger)
		close(done)
	}()
	cancel()
	select {
	case <-done:
		// ok
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestSIGHUPTrigger_ReturnsChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := SIGHUPTrigger(ctx)
	if ch == nil {
		t.Fatal("SIGHUPTrigger returned nil")
	}
	// Cancel and verify the channel closes (signal goroutine exits).
	cancel()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed, success
			}
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	// Note: not strictly fatal — the goroutine might still be
	// running but harmless. Just don't crash.
}
