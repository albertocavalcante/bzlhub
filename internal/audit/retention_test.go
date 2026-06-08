package audit

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/store"
)

func TestPruneAudit_RemovesOldRows(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	// Backdate one event by setting its timestamp manually via a
	// RecordAudit with an explicit Timestamp field.
	old := time.Now().Add(-72 * time.Hour).UTC()
	if err := s.RecordAudit(ctx, store.AuditEvent{
		Kind: "old_event", Timestamp: old, OK: true, Source: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAudit(ctx, store.AuditEvent{
		Kind: "fresh_event", OK: true, Source: "test",
	}); err != nil {
		t.Fatal(err)
	}

	n, err := s.PruneAudit(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want 1", n)
	}

	// The fresh event survives; old one is gone.
	rest, _ := s.ListAudit(ctx, store.AuditQuery{Limit: 100})
	if len(rest) != 1 {
		t.Fatalf("remaining = %d, want 1", len(rest))
	}
	if rest[0].Kind != "fresh_event" {
		t.Errorf("survivor kind = %q, want fresh_event", rest[0].Kind)
	}
}

func TestPruneAudit_ZeroDurationIsNoOp(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_ = s.RecordAudit(context.Background(), store.AuditEvent{Kind: "x", OK: true})

	n, err := s.PruneAudit(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("zero-duration prune returned %d, want 0", n)
	}
}

func TestDaemon_RunsPeriodically(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	d := NewRetentionDaemon(s, RetentionOptions{
		RetainDays: 1, // anything older than 24h is pruned
		Interval:   20 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	<-done

	if d.sweeps.Load() < 2 {
		t.Errorf("expected ≥2 sweeps in 200ms with 20ms interval, got %d", d.sweeps.Load())
	}
}

func TestDaemon_DisabledWhenRetainDaysIsZero(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	d := NewRetentionDaemon(s, RetentionOptions{RetainDays: 0, Interval: 10 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	d.Run(ctx)

	// RetainDays=0 means "never prune" — daemon returns immediately
	// without ever sweeping.
	if d.sweeps.Load() != 0 {
		t.Errorf("disabled daemon should not sweep; got %d", d.sweeps.Load())
	}
}

func TestDaemon_GracefulShutdown(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	d := NewRetentionDaemon(s, RetentionOptions{RetainDays: 1, Interval: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
		// ok
	case <-time.After(200 * time.Millisecond):
		t.Fatal("daemon did not return after ctx cancel")
	}
}

// sentinel to keep imports honest
var _ atomic.Int64
