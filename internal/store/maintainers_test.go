package store

import (
	"context"
	"errors"
	"testing"
)

func TestAddMaintainer_HappyPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.AddMaintainer(ctx, "rules_python", "alice@example.com", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	ok, err := s.IsMaintainer(ctx, "rules_python", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("IsMaintainer should be true after Add")
	}
}

func TestAddMaintainer_Idempotent(t *testing.T) {
	// Re-granting the same (module, email) is a no-op — operators
	// scripting grant runs shouldn't have to track "did I already?".
	// granted_by is NOT updated on re-grant (audit-friendly: the
	// first grant identity is preserved).
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.AddMaintainer(ctx, "rules_python", "alice@example.com", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMaintainer(ctx, "rules_python", "alice@example.com", "different@example.com"); err != nil {
		t.Fatalf("re-grant should not error: %v", err)
	}
	ms, _ := s.ListMaintainers(ctx, "rules_python")
	if len(ms) != 1 {
		t.Errorf("got %d rows, want 1 (re-grant must not double)", len(ms))
	}
	if ms[0].GrantedBy != "admin@example.com" {
		t.Errorf("granted_by changed on re-grant: %q (should remain original)", ms[0].GrantedBy)
	}
}

func TestIsMaintainer_NotPresent(t *testing.T) {
	s := newTestStore(t)
	ok, err := s.IsMaintainer(context.Background(), "rules_python", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("IsMaintainer should be false when not granted")
	}
}

func TestIsMaintainer_DifferentModule(t *testing.T) {
	// Maintainer of rules_python is NOT a maintainer of rules_go.
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.AddMaintainer(ctx, "rules_python", "alice@example.com", "admin@example.com")
	ok, _ := s.IsMaintainer(ctx, "rules_go", "alice@example.com")
	if ok {
		t.Error("maintainer of A must not be maintainer of B")
	}
}

func TestListMaintainers_OrderedByGrantedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.AddMaintainer(ctx, "rules_python", "alice@example.com", "admin@example.com")
	_ = s.AddMaintainer(ctx, "rules_python", "bob@example.com", "admin@example.com")
	_ = s.AddMaintainer(ctx, "rules_python", "carol@example.com", "admin@example.com")

	ms, err := s.ListMaintainers(ctx, "rules_python")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 {
		t.Fatalf("got %d, want 3", len(ms))
	}
	// All three should be alice, bob, carol in grant order (oldest first).
	emails := []string{ms[0].UserEmail, ms[1].UserEmail, ms[2].UserEmail}
	want := []string{"alice@example.com", "bob@example.com", "carol@example.com"}
	for i, w := range want {
		if emails[i] != w {
			t.Errorf("ms[%d].email = %q, want %q", i, emails[i], w)
		}
	}
}

func TestRemoveMaintainer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.AddMaintainer(ctx, "rules_python", "alice@example.com", "admin@example.com")
	if err := s.RemoveMaintainer(ctx, "rules_python", "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	ok, _ := s.IsMaintainer(ctx, "rules_python", "alice@example.com")
	if ok {
		t.Error("IsMaintainer should be false after Remove")
	}
}

func TestRemoveMaintainer_NotPresent(t *testing.T) {
	// Removing a non-existent grant is a no-op (idempotent).
	s := newTestStore(t)
	err := s.RemoveMaintainer(context.Background(), "rules_python", "alice@example.com")
	if err != nil {
		t.Errorf("remove of non-existent should be no-op, got %v", err)
	}
}

func TestAddMaintainer_RequiresFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cases := []struct {
		name, module, email, by string
	}{
		{"empty module", "", "alice@example.com", "admin@example.com"},
		{"empty email", "rules_python", "", "admin@example.com"},
		{"empty granted_by", "rules_python", "alice@example.com", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.AddMaintainer(ctx, c.module, c.email, c.by)
			if err == nil {
				t.Error("want error")
			}
		})
	}
}

func TestRemoveMaintainer_RequiresFields(t *testing.T) {
	s := newTestStore(t)
	err := s.RemoveMaintainer(context.Background(), "", "alice@example.com")
	if err == nil {
		t.Error("empty module should error")
	}
}

// negative-import sentinel
var _ = errors.New
