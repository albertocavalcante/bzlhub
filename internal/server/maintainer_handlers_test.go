package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/albertocavalcante/bzlhub/internal/auth"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// reach for the same env helper from the request_handlers tests.

func (e *requestTestEnv) postMaintainer(t *testing.T, module string, body any, id auth.Identity) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/modules/"+module+"/maintainers", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if id.IsAuthenticated() {
		req = req.WithContext(auth.WithContext(req.Context(), id))
	}
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("module", module)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
	w := httptest.NewRecorder()
	e.handlers.apiGrantMaintainer(w, req)
	return w
}

func (e *requestTestEnv) deleteMaintainer(t *testing.T, module, email string, id auth.Identity) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/modules/"+module+"/maintainers/"+email, nil)
	if id.IsAuthenticated() {
		req = req.WithContext(auth.WithContext(req.Context(), id))
	}
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("module", module)
	ctx.URLParams.Add("email", email)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
	w := httptest.NewRecorder()
	e.handlers.apiRevokeMaintainer(w, req)
	return w
}

func (e *requestTestEnv) listMaintainers(t *testing.T, module string, id auth.Identity) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/modules/"+module+"/maintainers", nil)
	if id.IsAuthenticated() {
		req = req.WithContext(auth.WithContext(req.Context(), id))
	}
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("module", module)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
	w := httptest.NewRecorder()
	e.handlers.apiListMaintainers(w, req)
	return w
}

func TestGrantMaintainer_DeniesWithoutGate_403(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.postMaintainer(t, "rules_python",
		map[string]string{"email": "alice@example.com"},
		bearerUser("alice@example.com"))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (no approver group)", w.Code)
	}
}

func TestGrantMaintainer_HappyPath(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.postMaintainer(t, "rules_python",
		map[string]string{"email": "alice@example.com"},
		bearerUser("admin@example.com", "approver"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	ok, _ := env.store.IsMaintainer(context.Background(), "rules_python", "alice@example.com")
	if !ok {
		t.Error("IsMaintainer should be true after grant")
	}

	// Audit event
	events, _ := env.store.ListAudit(context.Background(), store.AuditQuery{Kinds: []string{"maintainer_granted"}})
	if len(events) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(events))
	}
	if events[0].UserID != "admin@example.com" {
		t.Errorf("audit user_id = %q, want grantor's email", events[0].UserID)
	}
	if events[0].Module != "rules_python" {
		t.Errorf("audit module = %q", events[0].Module)
	}
}

func TestGrantMaintainer_MissingEmail_400(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.postMaintainer(t, "rules_python",
		map[string]string{}, // missing email
		bearerUser("admin@example.com", "approver"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestGrantMaintainer_IdempotentReGrant(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	first := env.postMaintainer(t, "rules_python",
		map[string]string{"email": "alice@example.com"},
		bearerUser("admin@example.com", "approver"))
	if first.Code != http.StatusOK {
		t.Fatal(first.Body.String())
	}
	// Re-grant — store's INSERT OR IGNORE makes it a no-op; handler
	// reports 200 either way.
	second := env.postMaintainer(t, "rules_python",
		map[string]string{"email": "alice@example.com"},
		bearerUser("admin@example.com", "approver"))
	if second.Code != http.StatusOK {
		t.Errorf("second status = %d, want 200 (idempotent)", second.Code)
	}
	// Still just one row.
	ms, _ := env.store.ListMaintainers(context.Background(), "rules_python")
	if len(ms) != 1 {
		t.Errorf("rows = %d, want 1", len(ms))
	}
}

func TestRevokeMaintainer_HappyPath(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	_ = env.store.AddMaintainer(context.Background(), "rules_python", "alice@example.com", "admin@example.com")

	w := env.deleteMaintainer(t, "rules_python", "alice@example.com",
		bearerUser("admin@example.com", "approver"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	ok, _ := env.store.IsMaintainer(context.Background(), "rules_python", "alice@example.com")
	if ok {
		t.Error("IsMaintainer should be false after revoke")
	}

	events, _ := env.store.ListAudit(context.Background(), store.AuditQuery{Kinds: []string{"maintainer_revoked"}})
	if len(events) != 1 {
		t.Errorf("audit rows = %d, want 1", len(events))
	}
}

func TestRevokeMaintainer_DeniesWithoutGate_403(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.deleteMaintainer(t, "rules_python", "alice@example.com",
		bearerUser("alice@example.com"))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestRevokeMaintainer_NotPresent_IsNoOp(t *testing.T) {
	// Delete of a non-existent grant returns 200 (the store layer is
	// idempotent; the handler doesn't probe first).
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.deleteMaintainer(t, "rules_python", "ghost@example.com",
		bearerUser("admin@example.com", "approver"))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestListMaintainers_HappyPath(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	ctx := context.Background()
	_ = env.store.AddMaintainer(ctx, "rules_python", "alice@example.com", "admin@example.com")
	_ = env.store.AddMaintainer(ctx, "rules_python", "bob@example.com", "admin@example.com")

	w := env.listMaintainers(t, "rules_python", auth.Anonymous())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		Maintainers []store.Maintainer `json:"maintainers"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Maintainers) != 2 {
		t.Errorf("got %d maintainers, want 2", len(resp.Maintainers))
	}
}

func TestListMaintainers_Empty_ReturnsEmptyArray(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.listMaintainers(t, "no_grants_here", auth.Anonymous())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	// Should serialize as an empty array, not null.
	var resp struct {
		Maintainers []store.Maintainer `json:"maintainers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Maintainers == nil {
		t.Errorf("maintainers serialized to null; want empty array")
	}
	if len(resp.Maintainers) != 0 {
		t.Errorf("len = %d, want 0", len(resp.Maintainers))
	}
}

func TestGrantMaintainer_EmptyModulePath_400(t *testing.T) {
	// Chi pattern won't actually let an empty module through, but the
	// handler defends against the case anyway.
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.postMaintainer(t, "",
		map[string]string{"email": "alice@example.com"},
		bearerUser("admin@example.com", "approver"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing module", w.Code)
	}
}
