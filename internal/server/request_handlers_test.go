package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/albertocavalcante/bzlhub/internal/auth"
	"github.com/albertocavalcante/bzlhub/internal/policy"
	"github.com/albertocavalcante/bzlhub/internal/ratelimit"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// requestTestEnv wires a real *store.Store + a *policy.Policy + the
// request handlers into one mux for end-to-end assertions. Keeps
// tests honest — the same SQL the production handler hits.
type requestTestEnv struct {
	store    *store.Store
	policy   *policy.Policy
	handlers *requestHandlers
}

func newRequestTestEnv(t *testing.T, p *policy.Policy) *requestTestEnv {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &requestTestEnv{
		store:    db,
		policy:   p,
		handlers: &requestHandlers{store: db, policy: policy.Static(p), log: slog.Default()},
	}
}

func (e *requestTestEnv) post(t *testing.T, path string, body any, id auth.Identity) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if id.IsAuthenticated() {
		req = req.WithContext(auth.WithContext(req.Context(), id))
	}
	w := httptest.NewRecorder()
	e.handlers.apiSubmitRequest(w, req)
	return w
}

func strictPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	p, err := policy.LoadProfile("strict")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func openPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	p, err := policy.LoadProfile("open")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func bearerUser(email string, groups ...string) auth.Identity {
	return auth.Identity{Email: email, Groups: groups, Source: auth.SourceBearer}
}

func TestSubmitRequest_AnonymousUnderStrict_403(t *testing.T) {
	// strict.auth.actions.submit_request = authenticated → anonymous denied.
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.5.0"},
		auth.Anonymous())
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestSubmitRequest_AnonymousUnderOpen_201(t *testing.T) {
	// open.auth.actions.submit_request = any → anonymous allowed.
	env := newRequestTestEnv(t, openPolicy(t))
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.5.0"},
		auth.Anonymous())
	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
}

func TestSubmitRequest_AuthenticatedUnderStrict_201(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.5.0"},
		bearerUser("alice@example.com"))
	if w.Code != http.StatusCreated {
		t.Errorf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ID    int64  `json:"id"`
		State string `json:"state"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ID == 0 {
		t.Errorf("id = 0")
	}
	if resp.State != "pending" {
		t.Errorf("state = %q, want pending", resp.State)
	}
}

func TestSubmitRequest_MissingModule_400(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.post(t, "/api/v1/requests",
		map[string]string{"version": "1.5.0"},
		bearerUser("alice@example.com"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSubmitRequest_DedupReturnsExisting_200(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	first := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.5.0"},
		bearerUser("alice@example.com"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first submit status = %d body=%s", first.Code, first.Body.String())
	}
	var firstResp struct{ ID int64 }
	_ = json.Unmarshal(first.Body.Bytes(), &firstResp)

	// Second submitter — different user, same coords — should collapse
	// onto first request's ID with 200 (not 201).
	second := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.5.0"},
		bearerUser("bob@example.com"))
	if second.Code != http.StatusOK {
		t.Errorf("dedup status = %d, want 200 body=%s", second.Code, second.Body.String())
	}
	var secondResp struct{ ID int64 }
	_ = json.Unmarshal(second.Body.Bytes(), &secondResp)
	if secondResp.ID != firstResp.ID {
		t.Errorf("dedup id = %d, want %d", secondResp.ID, firstResp.ID)
	}
}

func TestSubmitRequest_TerminalDoesNotDedup(t *testing.T) {
	// After a request reaches a terminal state (indexed/denied), a new
	// submit for the same coords starts a fresh request.
	env := newRequestTestEnv(t, strictPolicy(t))
	first := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.5.0"},
		bearerUser("alice@example.com"))
	var firstResp struct{ ID int64 }
	_ = json.Unmarshal(first.Body.Bytes(), &firstResp)

	// Drive it to denied.
	ctx := context.Background()
	_ = env.store.TransitionRequest(ctx, firstResp.ID, store.RequestStatePending, store.RequestStatePreflighting, nil)
	_ = env.store.TransitionRequest(ctx, firstResp.ID, store.RequestStatePreflighting, store.RequestStateDenied, &store.RequestFields{DenialReason: "test"})

	// Re-submit — should be a NEW request.
	second := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.5.0"},
		bearerUser("bob@example.com"))
	if second.Code != http.StatusCreated {
		t.Errorf("re-submit after terminal status = %d, want 201", second.Code)
	}
	var secondResp struct{ ID int64 }
	_ = json.Unmarshal(second.Body.Bytes(), &secondResp)
	if secondResp.ID == firstResp.ID {
		t.Errorf("re-submit returned same id %d as terminal one", secondResp.ID)
	}
}

func TestSubmitRequest_WritesAuditEvent(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.5.0"},
		bearerUser("alice@example.com"))
	if w.Code != http.StatusCreated {
		t.Fatalf("submit status = %d", w.Code)
	}
	events, err := env.store.ListAudit(context.Background(), store.AuditQuery{Kinds: []string{"request_submitted"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	if events[0].UserID != "alice@example.com" {
		t.Errorf("audit user_id = %q", events[0].UserID)
	}
	if events[0].Module != "rules_python" {
		t.Errorf("audit module = %q", events[0].Module)
	}
}

func TestSubmitRequest_RejectsMalformedJSON_400(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/requests", bytes.NewReader([]byte("{not json")))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithContext(req.Context(), bearerUser("alice@example.com")))
	w := httptest.NewRecorder()
	env.handlers.apiSubmitRequest(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSubmitRequest_ConcurrentDedup(t *testing.T) {
	// 10 simultaneous submits for the same coords. Plan 72 §C4
	// decision: collapse via app-level dedup. We accept a small race
	// window — two CreateRequest calls may both succeed if they
	// both miss the FindOpenRequest probe before either commits.
	// The test asserts the BOUND: at most a handful of distinct
	// IDs (not 10), and all are valid (no DB integrity errors).
	env := newRequestTestEnv(t, openPolicy(t))
	var wg sync.WaitGroup
	ids := make(map[int64]struct{})
	var mu sync.Mutex
	for range 10 {
		wg.Go(func() {
			w := env.post(t, "/api/v1/requests",
				map[string]string{"module": "rules_python", "version": "1.5.0"},
				auth.Anonymous())
			if w.Code != http.StatusCreated && w.Code != http.StatusOK {
				t.Errorf("concurrent submit status = %d", w.Code)
				return
			}
			var resp struct{ ID int64 }
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			mu.Lock()
			ids[resp.ID] = struct{}{}
			mu.Unlock()
		})
	}
	wg.Wait()
	// Under no contention we'd see 1 distinct ID. With unsynchronized
	// reads + writes the bound is ~workers. We assert "much less than
	// 10" to confirm dedup is meaningfully effective.
	if len(ids) > 5 {
		t.Errorf("dedup ineffective: %d distinct IDs from 10 concurrent submits", len(ids))
	}
}

// Helper: tests need ErrRequestNotFound to be reachable for negative
// assertions. This catches the "imports trimmed" foot-gun.
var _ = errors.Is(nil, store.ErrRequestNotFound)

// -- slice 3: list + detail ------------------------------------------

func (e *requestTestEnv) get(t *testing.T, path string, id auth.Identity) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if id.IsAuthenticated() {
		req = req.WithContext(auth.WithContext(req.Context(), id))
	}
	w := httptest.NewRecorder()
	switch {
	case strings.HasSuffix(req.URL.Path, "/requests"):
		e.handlers.apiListRequests(w, req)
	default:
		// Detail: /api/v1/requests/{id} — extract the last segment.
		idStr := strings.TrimPrefix(req.URL.Path, "/api/v1/requests/")
		ctx := chi.NewRouteContext()
		ctx.URLParams.Add("id", idStr)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
		e.handlers.apiGetRequest(w, req)
	}
	return w
}

func TestListRequests_GateDeniesAnonymousUnderClosed(t *testing.T) {
	// closed.auth.actions.view_requests = authenticated → anonymous 403.
	p, err := policy.LoadProfile("closed")
	if err != nil {
		t.Fatal(err)
	}
	env := newRequestTestEnv(t, p)
	w := env.get(t, "/api/v1/requests", auth.Anonymous())
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestListRequests_StrictAllowsAnonymous(t *testing.T) {
	// strict.auth.actions.view_requests = any → anonymous allowed.
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.get(t, "/api/v1/requests", auth.Anonymous())
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestListRequests_ReturnsPosted(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	// Submit 3 requests.
	for _, ver := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		w := env.post(t, "/api/v1/requests",
			map[string]string{"module": "rules_python", "version": ver},
			bearerUser("alice@example.com"))
		if w.Code != http.StatusCreated {
			t.Fatal(w.Body.String())
		}
	}
	w := env.get(t, "/api/v1/requests", auth.Anonymous())
	var resp struct {
		Requests []store.Request `json:"requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Requests) != 3 {
		t.Errorf("got %d requests, want 3", len(resp.Requests))
	}
}

func TestListRequests_FilterByState(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w1 := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.0.0"},
		bearerUser("alice@example.com"))
	var r1 struct{ ID int64 }
	_ = json.Unmarshal(w1.Body.Bytes(), &r1)
	// Move to preflighting so we have two distinct states.
	_ = env.store.TransitionRequest(context.Background(), r1.ID,
		store.RequestStatePending, store.RequestStatePreflighting, nil)
	_ = env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_go", "version": "0.50.0"},
		bearerUser("alice@example.com"))

	w := env.get(t, "/api/v1/requests?state=pending", auth.Anonymous())
	var resp struct {
		Requests []store.Request `json:"requests"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Requests) != 1 || resp.Requests[0].Module != "rules_go" {
		t.Errorf("?state=pending got %+v", resp.Requests)
	}
}

func TestListRequests_FilterBySubmitter(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	_ = env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.0.0"},
		bearerUser("alice@example.com"))
	_ = env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_go", "version": "0.50.0"},
		bearerUser("bob@example.com"))

	w := env.get(t, "/api/v1/requests?submitter=alice@example.com", auth.Anonymous())
	var resp struct {
		Requests []store.Request `json:"requests"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Requests) != 1 || resp.Requests[0].SubmitterSub != "alice@example.com" {
		t.Errorf("submitter filter got %+v", resp.Requests)
	}
}

func TestListRequests_LimitClampsToServerMax(t *testing.T) {
	// Client asks limit=99999 — server clamps to its 500 cap so the
	// JSON payload stays bounded.
	env := newRequestTestEnv(t, strictPolicy(t))
	for i := range 3 {
		_ = env.post(t, "/api/v1/requests",
			map[string]string{"module": "rules_python", "version": "1.0." + string(rune('0'+i))},
			bearerUser("alice@example.com"))
	}
	w := env.get(t, "/api/v1/requests?limit=99999", auth.Anonymous())
	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
	// Smoke: response decodes cleanly (the server didn't OOM or
	// return malformed JSON because of the absurd limit).
	var resp struct {
		Requests []store.Request `json:"requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
}

func TestGetRequest_Found(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.0.0"},
		bearerUser("alice@example.com"))
	var sub struct{ ID int64 }
	_ = json.Unmarshal(w.Body.Bytes(), &sub)

	wd := env.get(t, "/api/v1/requests/"+itoa(sub.ID), auth.Anonymous())
	if wd.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", wd.Code, wd.Body.String())
	}
	var got store.Request
	_ = json.Unmarshal(wd.Body.Bytes(), &got)
	if got.ID != sub.ID {
		t.Errorf("id = %d, want %d", got.ID, sub.ID)
	}
	if got.Module != "rules_python" {
		t.Errorf("module = %q", got.Module)
	}
}

func TestGetRequest_NotFound_404(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.get(t, "/api/v1/requests/9999", auth.Anonymous())
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestGetRequest_BadID_400(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.get(t, "/api/v1/requests/abc", auth.Anonymous())
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// -- slice 5: approve + deny -----------------------------------------

// policyWithActions builds a Policy with a custom auth.actions map
// for tests that need to assert specific gate behavior beyond what
// the strict/open/closed profiles allow.
func policyWithActions(actions map[string]policy.Gate) *policy.Policy {
	return &policy.Policy{Auth: policy.Auth{Actions: actions}}
}

func (e *requestTestEnv) postAction(t *testing.T, path string, body any, id auth.Identity, requestID int64) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if id.IsAuthenticated() {
		req = req.WithContext(auth.WithContext(req.Context(), id))
	}
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("id", itoa(requestID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
	w := httptest.NewRecorder()
	switch {
	case strings.HasSuffix(path, "/approve"):
		e.handlers.apiApproveRequest(w, req)
	case strings.HasSuffix(path, "/deny"):
		e.handlers.apiDenyRequest(w, req)
	}
	return w
}

func TestApproveRequest_DeniedWithoutGroup_403(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.0.0"},
		bearerUser("alice@example.com"))
	var sub struct{ ID int64 }
	_ = json.Unmarshal(w.Body.Bytes(), &sub)

	// strict: approve_request = group:approver. alice has no groups.
	got := env.postAction(t, "/api/v1/requests/"+itoa(sub.ID)+"/approve",
		nil, bearerUser("alice@example.com"), sub.ID)
	if got.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", got.Code)
	}
}

func TestApproveRequest_GroupMember_Approves(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	// Submit and drive to needs_review (only legal from-state for
	// approve per Plan 67).
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.0.0"},
		bearerUser("alice@example.com"))
	var sub struct{ ID int64 }
	_ = json.Unmarshal(w.Body.Bytes(), &sub)
	ctx := context.Background()
	_ = env.store.TransitionRequest(ctx, sub.ID, store.RequestStatePending, store.RequestStatePreflighting, nil)
	_ = env.store.TransitionRequest(ctx, sub.ID, store.RequestStatePreflighting, store.RequestStateNeedsReview, nil)

	got := env.postAction(t, "/api/v1/requests/"+itoa(sub.ID)+"/approve",
		nil, bearerUser("admin@example.com", "approver"), sub.ID)
	if got.Code != http.StatusOK {
		t.Errorf("status = %d body=%s", got.Code, got.Body.String())
	}
	after, _ := env.store.GetRequest(ctx, sub.ID)
	if after.State != store.RequestStateApproved {
		t.Errorf("state = %q, want approved", after.State)
	}
}

func TestApproveRequest_WrongState_409(t *testing.T) {
	// Approve is only legal from needs_review — calling it on a
	// pending request must return 409 (state machine refused).
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.0.0"},
		bearerUser("alice@example.com"))
	var sub struct{ ID int64 }
	_ = json.Unmarshal(w.Body.Bytes(), &sub)

	got := env.postAction(t, "/api/v1/requests/"+itoa(sub.ID)+"/approve",
		nil, bearerUser("admin@example.com", "approver"), sub.ID)
	if got.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", got.Code)
	}
}

func TestApproveRequest_NotFound_404(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	got := env.postAction(t, "/api/v1/requests/99999/approve",
		nil, bearerUser("admin@example.com", "approver"), 99999)
	if got.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", got.Code)
	}
}

func TestApproveRequest_WritesAuditEvent(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.0.0"},
		bearerUser("alice@example.com"))
	var sub struct{ ID int64 }
	_ = json.Unmarshal(w.Body.Bytes(), &sub)
	ctx := context.Background()
	_ = env.store.TransitionRequest(ctx, sub.ID, store.RequestStatePending, store.RequestStatePreflighting, nil)
	_ = env.store.TransitionRequest(ctx, sub.ID, store.RequestStatePreflighting, store.RequestStateNeedsReview, nil)

	_ = env.postAction(t, "/api/v1/requests/"+itoa(sub.ID)+"/approve",
		nil, bearerUser("admin@example.com", "approver"), sub.ID)
	events, _ := env.store.ListAudit(ctx, store.AuditQuery{Kinds: []string{"request_approved"}})
	if len(events) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(events))
	}
	if events[0].UserID != "admin@example.com" {
		t.Errorf("audit user_id = %q, want approver email", events[0].UserID)
	}
}

func TestDenyRequest_RequiresReason_400(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.0.0"},
		bearerUser("alice@example.com"))
	var sub struct{ ID int64 }
	_ = json.Unmarshal(w.Body.Bytes(), &sub)

	// Empty reason → 400. Denials without a justification are
	// hostile to the requester and useless to future auditors.
	got := env.postAction(t, "/api/v1/requests/"+itoa(sub.ID)+"/deny",
		map[string]string{"reason": ""},
		bearerUser("admin@example.com", "approver"), sub.ID)
	if got.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", got.Code)
	}
}

func TestDenyRequest_FromPending_Allowed(t *testing.T) {
	// deny is legal from pending, preflighting, needs_review.
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.0.0"},
		bearerUser("alice@example.com"))
	var sub struct{ ID int64 }
	_ = json.Unmarshal(w.Body.Bytes(), &sub)

	got := env.postAction(t, "/api/v1/requests/"+itoa(sub.ID)+"/deny",
		map[string]string{"reason": "license incompatible"},
		bearerUser("admin@example.com", "approver"), sub.ID)
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", got.Code, got.Body.String())
	}
	after, _ := env.store.GetRequest(context.Background(), sub.ID)
	if after.State != store.RequestStateDenied {
		t.Errorf("state = %q, want denied", after.State)
	}
	if after.DenialReason == "" {
		t.Error("denial_reason not persisted")
	}
}

func TestDenyRequest_FromTerminal_409(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.0.0"},
		bearerUser("alice@example.com"))
	var sub struct{ ID int64 }
	_ = json.Unmarshal(w.Body.Bytes(), &sub)
	ctx := context.Background()
	// Drive to indexed (terminal).
	_ = env.store.TransitionRequest(ctx, sub.ID, store.RequestStatePending, store.RequestStatePreflighting, nil)
	_ = env.store.TransitionRequest(ctx, sub.ID, store.RequestStatePreflighting, store.RequestStateAutoPass, nil)
	_ = env.store.TransitionRequest(ctx, sub.ID, store.RequestStateAutoPass, store.RequestStateFetching, nil)
	_ = env.store.TransitionRequest(ctx, sub.ID, store.RequestStateFetching, store.RequestStateIndexed, nil)

	got := env.postAction(t, "/api/v1/requests/"+itoa(sub.ID)+"/deny",
		map[string]string{"reason": "too late"},
		bearerUser("admin@example.com", "approver"), sub.ID)
	if got.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (denied from terminal state)", got.Code)
	}
}

func TestDenyRequest_DeniesAnonymous_403(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.0.0"},
		bearerUser("alice@example.com"))
	var sub struct{ ID int64 }
	_ = json.Unmarshal(w.Body.Bytes(), &sub)

	got := env.postAction(t, "/api/v1/requests/"+itoa(sub.ID)+"/deny",
		map[string]string{"reason": "no"},
		auth.Anonymous(), sub.ID)
	if got.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", got.Code)
	}
}

// Custom-policy edge case: policy that explicitly allows alice to
// approve. Pins that the gate logic flows the right identity through.
func TestApproveRequest_CustomPolicyAllows(t *testing.T) {
	p := policyWithActions(map[string]policy.Gate{
		"view_requests":   policy.GateAny,
		"submit_request":  policy.GateAuthenticated,
		"approve_request": "group:reviewers",
	})
	env := newRequestTestEnv(t, p)
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_python", "version": "1.0.0"},
		bearerUser("alice@example.com"))
	var sub struct{ ID int64 }
	_ = json.Unmarshal(w.Body.Bytes(), &sub)
	ctx := context.Background()
	_ = env.store.TransitionRequest(ctx, sub.ID, store.RequestStatePending, store.RequestStatePreflighting, nil)
	_ = env.store.TransitionRequest(ctx, sub.ID, store.RequestStatePreflighting, store.RequestStateNeedsReview, nil)

	got := env.postAction(t, "/api/v1/requests/"+itoa(sub.ID)+"/approve",
		nil, bearerUser("alice@example.com", "reviewers"), sub.ID)
	if got.Code != http.StatusOK {
		t.Errorf("status = %d", got.Code)
	}
}

// -- slice 6: GET /api/v1/policy/effective --------------------------

func (e *requestTestEnv) getEffective(t *testing.T, id auth.Identity) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/policy/effective", nil)
	if id.IsAuthenticated() {
		req = req.WithContext(auth.WithContext(req.Context(), id))
	}
	w := httptest.NewRecorder()
	e.handlers.apiPolicyEffective(w, req)
	return w
}

func TestPolicyEffective_Anonymous(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.getEffective(t, auth.Anonymous())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		Profile  string            `json:"profile"`
		Actions  map[string]bool   `json:"actions"`
		Identity map[string]any    `json:"identity"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Profile != "strict" {
		t.Errorf("profile = %q", resp.Profile)
	}
	// strict baseline: view_modules=any (anonymous allowed),
	// submit_request=authenticated (anonymous denied).
	if !resp.Actions["view_modules"] {
		t.Error("anonymous should see view_modules=true under strict")
	}
	if resp.Actions["submit_request"] {
		t.Error("anonymous should see submit_request=false under strict")
	}
	if resp.Actions["approve_request"] {
		t.Error("anonymous should see approve_request=false")
	}
	// Identity field present even for anonymous; source=anonymous.
	if resp.Identity == nil {
		t.Fatal("response missing identity field")
	}
	if resp.Identity["source"] != "anonymous" {
		t.Errorf("identity.source = %v, want anonymous", resp.Identity["source"])
	}
	if email, _ := resp.Identity["email"].(string); email != "" {
		t.Errorf("anonymous identity.email = %q, want empty", email)
	}
}

func TestPolicyEffective_BearerCarriesIdentity(t *testing.T) {
	// Identity field reflects the resolved bearer identity so the
	// UI can render "signed in as ..." without a separate /whoami
	// call.
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.getEffective(t, bearerUser("alice@example.com", "approver"))
	var resp struct {
		Identity map[string]any `json:"identity"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Identity["source"] != "bearer" {
		t.Errorf("identity.source = %v, want bearer", resp.Identity["source"])
	}
	if resp.Identity["email"] != "alice@example.com" {
		t.Errorf("identity.email = %v", resp.Identity["email"])
	}
	groups, _ := resp.Identity["groups"].([]any)
	if len(groups) != 1 || groups[0] != "approver" {
		t.Errorf("identity.groups = %v", resp.Identity["groups"])
	}
}

func TestPolicyEffective_Authenticated(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.getEffective(t, bearerUser("alice@example.com"))
	var resp struct {
		Actions map[string]bool `json:"actions"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Actions["submit_request"] {
		t.Error("authenticated user should see submit_request=true")
	}
	// alice has no groups → no approve_request.
	if resp.Actions["approve_request"] {
		t.Error("non-group user should see approve_request=false")
	}
}

func TestPolicyEffective_GroupMember(t *testing.T) {
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.getEffective(t, bearerUser("admin@example.com", "approver"))
	var resp struct {
		Actions map[string]bool `json:"actions"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Actions["approve_request"] {
		t.Error("approver-group user should see approve_request=true")
	}
}

func TestPolicyEffective_MaintainerGateAlwaysFalse(t *testing.T) {
	// maintain_module is per-target. The /effective endpoint reports
	// the GLOBAL view — it can't know which target the UI is asking
	// about, so it reports false. UI calls AllowFor on the actual
	// page (chunk 4 followup).
	env := newRequestTestEnv(t, strictPolicy(t))
	w := env.getEffective(t, bearerUser("alice@example.com"))
	var resp struct {
		Actions map[string]bool `json:"actions"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Actions["maintain_module"] {
		t.Error("maintain_module must be false in /effective (per-target gate)")
	}
}

// ---------- Per-user rate-limit (Plan 75 slice ζ) ----------

// newRequestTestEnvRateLimited mirrors newRequestTestEnv but wires a
// UserLimiter into the handler so the ζ rate-limit path is
// exercised. The policy snapshot's per_user_rate_limit field
// determines the rate at request time (default open profile sets
// "10/hour").
func newRequestTestEnvRateLimited(t *testing.T, p *policy.Policy) *requestTestEnv {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &requestTestEnv{
		store:  db,
		policy: p,
		handlers: &requestHandlers{
			store:    db,
			policy:   policy.Static(p),
			log:      slog.Default(),
			userRate: ratelimit.NewUserLimiter(),
		},
	}
}

func TestSubmitRequest_PerUserRateLimit_Exceeded_429(t *testing.T) {
	p := openPolicy(t)
	p.Auth.PerUserRateLimit = "3/hour"
	env := newRequestTestEnvRateLimited(t, p)

	alice := bearerUser("alice@example.com")
	// First 3 should succeed (or dedup), 4th should rate-limit.
	for i := 0; i < 3; i++ {
		w := env.post(t, "/api/v1/requests",
			map[string]string{"module": "rules_x", "version": "1." + itoa(int64(i))},
			alice)
		if w.Code != http.StatusCreated && w.Code != http.StatusOK {
			t.Fatalf("attempt %d: status=%d, want 201/200", i, w.Code)
		}
	}
	w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "rules_x", "version": "1.99"},
		alice)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("4th attempt status=%d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing on 429")
	}
}

func TestSubmitRequest_PerUserRateLimit_DifferentUsersIndependent(t *testing.T) {
	p := openPolicy(t)
	p.Auth.PerUserRateLimit = "1/hour"
	env := newRequestTestEnvRateLimited(t, p)

	alice := bearerUser("alice@example.com")
	bob := bearerUser("bob@example.com")

	if w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "m1", "version": "1.0"}, alice); w.Code != http.StatusCreated {
		t.Fatalf("alice first: status=%d, want 201", w.Code)
	}
	if w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "m2", "version": "1.0"}, alice); w.Code != http.StatusTooManyRequests {
		t.Fatalf("alice second: status=%d, want 429", w.Code)
	}
	// Bob still has a token.
	if w := env.post(t, "/api/v1/requests",
		map[string]string{"module": "m3", "version": "1.0"}, bob); w.Code != http.StatusCreated {
		t.Fatalf("bob first: status=%d, want 201 (independent bucket)", w.Code)
	}
}

func TestSubmitRequest_PerUserRateLimit_UnsetSkipsGate(t *testing.T) {
	p := openPolicy(t)
	p.Auth.PerUserRateLimit = "" // explicit unset
	env := newRequestTestEnvRateLimited(t, p)

	alice := bearerUser("alice@example.com")
	// Burst 20 submits should all pass with no rate configured.
	for i := 0; i < 20; i++ {
		w := env.post(t, "/api/v1/requests",
			map[string]string{"module": "rules_y", "version": "1." + itoa(int64(i))},
			alice)
		if w.Code != http.StatusCreated && w.Code != http.StatusOK {
			t.Fatalf("attempt %d: status=%d, want 201/200 (unset rate)", i, w.Code)
		}
	}
}

// itoa avoids importing strconv just for the test path-building.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
