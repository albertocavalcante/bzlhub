package policy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/auth"
)

// fakeMaintainerSource is a controllable stub for the Evaluator's
// MaintainerSource. Counts calls so we can assert cache behavior.
type fakeMaintainerSource struct {
	mu       sync.Mutex
	grants   map[string]map[string]bool // module → email → granted?
	calls    int64
	failNext bool
}

func newFakeMS() *fakeMaintainerSource {
	return &fakeMaintainerSource{grants: map[string]map[string]bool{}}
}

func (f *fakeMaintainerSource) grant(module, email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.grants[module] == nil {
		f.grants[module] = map[string]bool{}
	}
	f.grants[module][email] = true
}

func (f *fakeMaintainerSource) IsMaintainer(_ context.Context, module, email string) (bool, error) {
	atomic.AddInt64(&f.calls, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return false, errors.New("simulated db failure")
	}
	if f.grants[module] == nil {
		return false, nil
	}
	return f.grants[module][email], nil
}

func (f *fakeMaintainerSource) Calls() int64 {
	return atomic.LoadInt64(&f.calls)
}

func evalUser(email string, groups ...string) auth.Identity {
	return auth.Identity{Email: email, Groups: groups, Source: auth.SourceBearer}
}

func newEvaluator(p *Policy, ms MaintainerSource) *Evaluator {
	return NewEvaluator(p, ms, EvaluatorOptions{CacheTTL: 50 * time.Millisecond, CacheMax: 32})
}

// -- AllowFor (target-aware) -----------------------------------------

func TestEvaluator_AllowFor_MaintainerGate(t *testing.T) {
	p := &Policy{Auth: Auth{Actions: map[string]Gate{
		"maintain_module": GateMaintainer,
	}}}
	ms := newFakeMS()
	ms.grant("rules_python", "alice@example.com")
	e := newEvaluator(p, ms)

	if !e.AllowFor(context.Background(), evalUser("alice@example.com"), "maintain_module", "rules_python") {
		t.Error("maintainer must be allowed")
	}
	if e.AllowFor(context.Background(), evalUser("bob@example.com"), "maintain_module", "rules_python") {
		t.Error("non-maintainer must be denied")
	}
	if e.AllowFor(context.Background(), evalUser("alice@example.com"), "maintain_module", "rules_go") {
		t.Error("maintainer of A must not be maintainer of B")
	}
	if e.AllowFor(context.Background(), auth.Anonymous(), "maintain_module", "rules_python") {
		t.Error("anonymous must never be a maintainer")
	}
}

func TestEvaluator_AllowFor_NonMaintainerActionDelegatesToAllow(t *testing.T) {
	// AllowFor for a non-maintainer gate should behave identically
	// to Allow — the target is ignored.
	p := &Policy{Auth: Auth{Actions: map[string]Gate{
		"submit_request": GateAuthenticated,
	}}}
	ms := newFakeMS()
	e := newEvaluator(p, ms)

	if !e.AllowFor(context.Background(), evalUser("alice@example.com"), "submit_request", "any_target") {
		t.Error("authenticated user should pass authenticated gate via AllowFor")
	}
	if e.AllowFor(context.Background(), auth.Anonymous(), "submit_request", "any_target") {
		t.Error("anonymous should fail authenticated gate via AllowFor")
	}
	// Crucially: the maintainer source should NEVER be consulted for
	// non-maintainer gates.
	if ms.Calls() != 0 {
		t.Errorf("MaintainerSource consulted %d times for non-maintainer gate", ms.Calls())
	}
}

func TestEvaluator_AllowFor_UnknownGateDenies(t *testing.T) {
	p := &Policy{Auth: Auth{Actions: map[string]Gate{}}}
	e := newEvaluator(p, newFakeMS())
	if e.AllowFor(context.Background(), evalUser("alice@example.com"), "ghost_action", "x") {
		t.Error("unknown action must default to deny")
	}
}

// -- Cache behavior --------------------------------------------------

func TestEvaluator_Cache_HitWithinTTL(t *testing.T) {
	p := &Policy{Auth: Auth{Actions: map[string]Gate{"maintain_module": GateMaintainer}}}
	ms := newFakeMS()
	ms.grant("rules_python", "alice@example.com")
	e := newEvaluator(p, ms)
	ctx := context.Background()

	for range 10 {
		if !e.AllowFor(ctx, evalUser("alice@example.com"), "maintain_module", "rules_python") {
			t.Fatal("expected maintainer to be allowed")
		}
	}
	if got := ms.Calls(); got != 1 {
		t.Errorf("MaintainerSource calls = %d, want 1 (rest should be cache hits)", got)
	}
}

func TestEvaluator_Cache_ExpiresAfterTTL(t *testing.T) {
	p := &Policy{Auth: Auth{Actions: map[string]Gate{"maintain_module": GateMaintainer}}}
	ms := newFakeMS()
	ms.grant("rules_python", "alice@example.com")
	e := NewEvaluator(p, ms, EvaluatorOptions{CacheTTL: 20 * time.Millisecond, CacheMax: 32})
	ctx := context.Background()

	_ = e.AllowFor(ctx, evalUser("alice@example.com"), "maintain_module", "rules_python")
	time.Sleep(30 * time.Millisecond) // exceed TTL
	_ = e.AllowFor(ctx, evalUser("alice@example.com"), "maintain_module", "rules_python")

	if ms.Calls() != 2 {
		t.Errorf("calls = %d, want 2 (one before TTL, one after)", ms.Calls())
	}
}

func TestEvaluator_Cache_Invalidate(t *testing.T) {
	p := &Policy{Auth: Auth{Actions: map[string]Gate{"maintain_module": GateMaintainer}}}
	ms := newFakeMS()
	ms.grant("rules_python", "alice@example.com")
	e := newEvaluator(p, ms)
	ctx := context.Background()

	_ = e.AllowFor(ctx, evalUser("alice@example.com"), "maintain_module", "rules_python")
	if ms.Calls() != 1 {
		t.Fatalf("setup: calls = %d", ms.Calls())
	}
	e.InvalidateMaintainer("alice@example.com", "rules_python")
	_ = e.AllowFor(ctx, evalUser("alice@example.com"), "maintain_module", "rules_python")
	if ms.Calls() != 2 {
		t.Errorf("after invalidate, calls = %d, want 2", ms.Calls())
	}
}

func TestEvaluator_Cache_NegativeResultCached(t *testing.T) {
	// Cache should remember "not a maintainer" too — defending against
	// repeated probes for non-maintainers (the common case in audit
	// lookups + UI re-renders).
	p := &Policy{Auth: Auth{Actions: map[string]Gate{"maintain_module": GateMaintainer}}}
	ms := newFakeMS()
	e := newEvaluator(p, ms)
	ctx := context.Background()

	for range 10 {
		if e.AllowFor(ctx, evalUser("alice@example.com"), "maintain_module", "rules_python") {
			t.Fatal("should be denied")
		}
	}
	if ms.Calls() != 1 {
		t.Errorf("negative-cache calls = %d, want 1", ms.Calls())
	}
}

func TestEvaluator_Cache_BoundedSize(t *testing.T) {
	// When the cache hits CacheMax, the oldest entries are evicted.
	// We just assert that adding many more entries than the cap
	// doesn't OOM the test and re-probes happen for evicted entries.
	p := &Policy{Auth: Auth{Actions: map[string]Gate{"maintain_module": GateMaintainer}}}
	ms := newFakeMS()
	e := NewEvaluator(p, ms, EvaluatorOptions{CacheTTL: time.Minute, CacheMax: 4})
	ctx := context.Background()

	for i := range 20 {
		mod := fmt.Sprintf("mod_%d", i)
		_ = e.AllowFor(ctx, evalUser("alice@example.com"), "maintain_module", mod)
	}
	// After overflow, probing the FIRST module again should re-call
	// the source (evicted).
	calls := ms.Calls()
	_ = e.AllowFor(ctx, evalUser("alice@example.com"), "maintain_module", "mod_0")
	if ms.Calls() == calls {
		t.Error("evicted entry should re-probe source")
	}
}

// -- Source error handling --------------------------------------------

func TestEvaluator_AllowFor_SourceErrorDeniesSafely(t *testing.T) {
	// MaintainerSource failures (DB down, transient) should DENY the
	// action — defaulting open on lookup failure is a security
	// regression. The Evaluator must NOT cache the error verdict so
	// the next call gets a fresh probe.
	p := &Policy{Auth: Auth{Actions: map[string]Gate{"maintain_module": GateMaintainer}}}
	ms := newFakeMS()
	ms.grant("rules_python", "alice@example.com")
	ms.failNext = true
	e := newEvaluator(p, ms)

	if e.AllowFor(context.Background(), evalUser("alice@example.com"), "maintain_module", "rules_python") {
		t.Error("source error must deny (no fail-open)")
	}
	// Recovery: next call (without failNext) should succeed AND probe
	// the source (error verdict not cached).
	if !e.AllowFor(context.Background(), evalUser("alice@example.com"), "maintain_module", "rules_python") {
		t.Error("recovery call should succeed")
	}
}

// -- Concurrency ------------------------------------------------------

func TestEvaluator_Concurrent_NoRace(t *testing.T) {
	p := &Policy{Auth: Auth{Actions: map[string]Gate{"maintain_module": GateMaintainer}}}
	ms := newFakeMS()
	for i := range 5 {
		ms.grant(fmt.Sprintf("mod_%d", i), "alice@example.com")
	}
	e := newEvaluator(p, ms)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for i := range 100 {
				mod := fmt.Sprintf("mod_%d", i%5)
				_ = e.AllowFor(context.Background(), evalUser("alice@example.com"), "maintain_module", mod)
				if i%10 == 0 {
					e.InvalidateMaintainer("alice@example.com", mod)
				}
			}
		})
	}
	wg.Wait()
}
