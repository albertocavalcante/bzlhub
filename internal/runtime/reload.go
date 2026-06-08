// Package runtime hosts long-running orchestration primitives shared
// across canopy's serve loop. Today: Reloader drives SIGHUP-triggered
// configuration reloads (bearer identity, policy). Future: anything
// that fits the "spawn one goroutine in serve.go, runs forever, ctx
// cancellation stops it" shape.
package runtime

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// Reloader runs registered reload functions on every trigger event
// (in production: SIGHUP). Each registered function is associated
// with a name used for orchestrator-level logging; the function
// itself is responsible for the rich per-reload logging (what
// changed, diagnostics, counts).
//
// Failed reloads keep the current value — never accept malformed
// state. The orchestrator logs a Warn line; subsequent triggers
// continue to invoke every registered function (one bad reloader
// does not poison the rest).
//
// Reloader is NOT safe for concurrent Register after Run starts.
// Register every reloader before spawning the goroutine.
type Reloader struct {
	log *slog.Logger
	fns []namedReload
}

type namedReload struct {
	name string
	fn   func(ctx context.Context) error
}

// NewReloader constructs a Reloader. log is required (use
// slog.Default() if no scoped logger is available).
func NewReloader(log *slog.Logger) *Reloader {
	if log == nil {
		log = slog.Default()
	}
	return &Reloader{log: log}
}

// Register adds a reload function. fn is invoked once per trigger
// event, in registration order. fn should return nil on success and
// an error to keep the prior value. fn is responsible for its own
// success-path logging (e.g., "policy reloaded path=… profile=…").
func (r *Reloader) Register(name string, fn func(ctx context.Context) error) {
	r.fns = append(r.fns, namedReload{name: name, fn: fn})
}

// HasReloaders reports whether any reloader is registered. Callers
// use this to skip spawning the Run goroutine when no reload work
// is wired (e.g., neither identity nor policy file was configured).
func (r *Reloader) HasReloaders() bool { return len(r.fns) > 0 }

// Run blocks until ctx is cancelled. On every trigger event it
// invokes every registered reload function in order. Failures are
// logged but do not stop subsequent reloaders or future triggers.
//
// The trigger channel is typically the result of SIGHUPTrigger(ctx)
// in production; tests pass a channel they control directly so the
// reload path is exercised without OS signal flakiness (Plan 76
// §4.3 documents the macOS signal-test flake).
func (r *Reloader) Run(ctx context.Context, trigger <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-trigger:
			if !ok {
				return
			}
			r.runOnce(ctx)
		}
	}
}

func (r *Reloader) runOnce(ctx context.Context) {
	for _, n := range r.fns {
		if err := n.fn(ctx); err != nil {
			r.log.Warn("reload failed, keeping current value",
				"name", n.name, "err", err)
		}
	}
}

// SIGHUPTrigger returns a channel that emits one event per received
// SIGHUP. Coalesces bursts: if a SIGHUP arrives while a prior event
// is still buffered, it is dropped (consumers see one trigger per
// "operator-edited-and-sent-SIGHUP" episode regardless of debounce
// timing).
//
// The signal subscription is released when ctx cancels. SIGHUP is
// not portable to Windows; canopy's serve loop is Linux/macOS-only
// per existing convention.
func SIGHUPTrigger(ctx context.Context) <-chan struct{} {
	out := make(chan struct{}, 1)
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	go func() {
		defer signal.Stop(hupCh)
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case <-hupCh:
				select {
				case out <- struct{}{}:
				default:
					// Coalesce; consumer hasn't drained yet.
				}
			}
		}
	}()
	return out
}
