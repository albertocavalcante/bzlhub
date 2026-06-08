// Procurement request handlers — submit, list, get, approve, deny,
// plus the per-caller /policy/effective view.
//
// Identity comes from the bearer / header middleware; the policy
// gate decides whether the caller is allowed to perform the action.
// App-level dedup collapses concurrent same-coord submissions into
// the existing open request thread.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/albertocavalcante/bzlhub/internal/auth"
	"github.com/albertocavalcante/bzlhub/internal/policy"
	"github.com/albertocavalcante/bzlhub/internal/ratelimit"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// requestHandlers groups the procurement HTTP surface. Routes are
// registered only when both store and policy are wired.
type requestHandlers struct {
	store    requestStore
	policy   policy.Snapshot
	log      *slog.Logger
	userRate *ratelimit.UserLimiter // nil disables per-user gate (e.g., tests)
}

// requestStore is the slice of *store.Store the handlers touch.
// Interface keeps tests independent of SQLite.
type requestStore interface {
	CreateRequest(ctx context.Context, r store.Request) (int64, error)
	GetRequest(ctx context.Context, id int64) (*store.Request, error)
	ListRequests(ctx context.Context, q store.RequestQuery) ([]*store.Request, error)
	FindOpenRequest(ctx context.Context, module, version string) (*store.Request, error)
	TransitionRequest(ctx context.Context, id int64, from, to store.RequestState, fields *store.RequestFields) error
	RecordAudit(ctx context.Context, ev store.AuditEvent) error

	// Maintainer management (slice 1D — Plan 73 §10).
	AddMaintainer(ctx context.Context, module, userEmail, grantedBy string) error
	RemoveMaintainer(ctx context.Context, module, userEmail string) error
	IsMaintainer(ctx context.Context, module, userEmail string) (bool, error)
	ListMaintainers(ctx context.Context, module string) ([]store.Maintainer, error)
}

// apiSubmitRequest handles POST /api/v1/requests.
//
// Body shape:
//
//	{
//	  "module":  "rules_python",
//	  "version": "1.5.0",
//	  "source_url": "https://...",   // optional; preflight figures
//	                                  // it out from BCR when absent
//	  "notes":   "needed for X"      // optional
//	}
//
// Responses:
//   - 201 + {id, state:"pending"}  → new request created
//   - 200 + {id, state}             → dedup hit on an open request
//   - 400                           → malformed body or missing fields
//   - 403                           → policy gate denied
//   - 500                           → store failure
func (h *requestHandlers) apiSubmitRequest(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	pol := h.policy()
	if !pol.Allow(id, "submit_request") {
		denyByPolicy(w, "submit_request")
		return
	}
	if !h.checkUserRate(w, r, pol, id) {
		return
	}
	body, ok := parseSubmitBody(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if existing := h.findDedupTarget(w, ctx, body.Module, body.Version); existing != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"id":    existing.ID,
			"state": existing.State,
			"dedup": true,
		})
		return
	}
	newID, err := h.store.CreateRequest(ctx, newRequestFromBody(id, body))
	if err != nil {
		h.log.Error("submit_request: CreateRequest failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.recordSubmitAudit(ctx, r, id, newID, body)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":    newID,
		"state": store.RequestStatePending,
	})
}

// submitBody is the parsed POST /api/v1/requests body.
type submitBody struct {
	Module    string `json:"module"`
	Version   string `json:"version"`
	SourceURL string `json:"source_url,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

// parseSubmitBody decodes + validates the submit body. On invalid
// input it writes the 400 response itself and returns ok=false so
// the caller's flow stays linear.
func parseSubmitBody(w http.ResponseWriter, r *http.Request) (submitBody, bool) {
	var body submitBody
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return body, false
	}
	body.Module = strings.TrimSpace(body.Module)
	body.Version = strings.TrimSpace(body.Version)
	if body.Module == "" || body.Version == "" {
		http.Error(w, "bad request: module and version are required", http.StatusBadRequest)
		return body, false
	}
	return body, true
}

// findDedupTarget returns the existing open request for
// (module, version), or nil when there's none. On store errors
// other than not-found it writes a 500 response.
//
// The unsynchronized read-then-write race window is bounded by
// TestSubmitRequest_ConcurrentDedup.
func (h *requestHandlers) findDedupTarget(w http.ResponseWriter, ctx context.Context, module, version string) *store.Request {
	existing, err := h.store.FindOpenRequest(ctx, module, version)
	if err == nil {
		return existing
	}
	if errors.Is(err, store.ErrRequestNotFound) {
		return nil
	}
	h.log.Error("submit_request: FindOpenRequest failed", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
	return nil
}

// newRequestFromBody shapes a store.Request from the parsed submit
// body and the caller's identity.
func newRequestFromBody(id auth.Identity, body submitBody) store.Request {
	authMethod := "anonymous"
	if id.IsAuthenticated() {
		authMethod = string(id.Source)
	}
	return store.Request{
		SubmitterSub:   submitterSub(id),
		SubmitterEmail: id.Email,
		AuthMethod:     authMethod,
		Module:         body.Module,
		Version:        body.Version,
		SourceURL:      body.SourceURL,
		SubmitterNotes: body.Notes,
	}
}

// checkUserRate consults the per-user rate limiter (if configured)
// against policy.Auth.PerUserRateLimit. Returns true to let the
// caller proceed; false when the limiter rejected the request and a
// 429 response was written.
//
// The limiter and the rate string are kept decoupled: the limiter
// stores a per-user bucket; the policy snapshot supplies the rate on
// every call, so a SIGHUP-driven rate change takes effect on the
// next request without rebuilding the limiter.
//
// Anonymous submitters share a single bucket keyed by the literal
// "anonymous" submitter_sub — this is intentional. An open profile
// allowing anonymous submit should configure a stricter
// per_user_rate_limit *and* the anonymous_abuse.per_ip_rate_limit
// (which the front-of-server limiter handles separately).
func (h *requestHandlers) checkUserRate(w http.ResponseWriter, r *http.Request, pol *policy.Policy, id auth.Identity) bool {
	if h.userRate == nil {
		return true
	}
	count, per, err := ratelimit.ParseRate(pol.Auth.PerUserRateLimit)
	if err != nil {
		if !errors.Is(err, ratelimit.ErrRateUnset) {
			h.log.Warn("policy per_user_rate_limit malformed; skipping per-user limiter",
				"value", pol.Auth.PerUserRateLimit, "err", err)
		}
		return true
	}
	user := submitterSub(id)
	ok, retryAfter := h.userRate.Allow(user, count, per)
	if ok {
		return true
	}
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	http.Error(w,
		fmt.Sprintf("rate limit exceeded; try again in %ds", seconds),
		http.StatusTooManyRequests)
	h.log.Info("per-user rate limit triggered",
		"user", user,
		"path", r.URL.Path,
		"rate", pol.Auth.PerUserRateLimit,
		"retry_after_s", seconds)
	return false
}

// recordSubmitAudit writes the request_submitted audit event.
// Failures are logged but don't fail the request — a transient SQL
// hiccup shouldn't reject a successful submit.
func (h *requestHandlers) recordSubmitAudit(ctx context.Context, r *http.Request, id auth.Identity, newID int64, body submitBody) {
	err := h.store.RecordAudit(ctx, store.AuditEvent{
		Kind:    "request_submitted",
		Source:  sourceTag(r, "rest"),
		Module:  body.Module,
		Version: body.Version,
		OK:      true,
		UserID:  id.DisplayName(),
		Payload: mustMarshal(map[string]any{
			"request_id": newID,
			"source_url": body.SourceURL,
		}),
	})
	if err != nil {
		h.log.Warn("submit_request: audit write failed (request still created)",
			"err", err, "request_id", newID)
	}
}

// apiListRequests handles GET /api/v1/requests.
//
// Query parameters:
//   - state    (repeated) — exact-match any-of against RequestState
//   - submitter             — exact-match against submitter_sub
//   - limit                 — server-capped at 500
//
// Gate: `view_requests`. Returns 403 when denied.
func (h *requestHandlers) apiListRequests(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if !h.policy().Allow(id, "view_requests") {
		denyByPolicy(w, "view_requests")
		return
	}

	q := r.URL.Query()
	var states []store.RequestState
	for _, s := range q["state"] {
		s = strings.TrimSpace(s)
		if s != "" {
			states = append(states, store.RequestState(s))
		}
	}
	limit := 0
	if l := strings.TrimSpace(q.Get("limit")); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}

	rows, err := h.store.ListRequests(r.Context(), store.RequestQuery{
		States:    states,
		Submitter: strings.TrimSpace(q.Get("submitter")),
		Limit:     limit,
	})
	if err != nil {
		h.log.Error("list_requests: store failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Always emit `requests: []` (not null) so clients don't have to
	// branch on nil-vs-empty.
	if rows == nil {
		rows = []*store.Request{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": rows})
}

// apiGetRequest handles GET /api/v1/requests/{id}.
//
// Gate: `view_requests`. Returns 404 on unknown id, 400 on
// non-numeric id, 403 when denied.
func (h *requestHandlers) apiGetRequest(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if !h.policy().Allow(id, "view_requests") {
		denyByPolicy(w, "view_requests")
		return
	}

	idStr := chi.URLParam(r, "id")
	reqID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || reqID <= 0 {
		http.Error(w, "bad request: id must be a positive integer", http.StatusBadRequest)
		return
	}
	got, err := h.store.GetRequest(r.Context(), reqID)
	if err != nil {
		if errors.Is(err, store.ErrRequestNotFound) {
			http.Error(w, "request not found", http.StatusNotFound)
			return
		}
		h.log.Error("get_request: store failed", "err", err, "id", reqID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// apiApproveRequest handles POST /api/v1/requests/{id}/approve.
//
// Legal from-state: needs_review only. Auto-pass requests don't
// flow through here — they advance to fetching automatically.
//
// Gate: `approve_request`. Returns 403 when denied, 404 when id
// unknown, 409 when the request is in any state other than
// needs_review, 200 on success.
func (h *requestHandlers) apiApproveRequest(w http.ResponseWriter, r *http.Request) {
	h.transitionByReviewer(w, r, transitionConfig{
		action:        "approve_request",
		auditKind:     "request_approved",
		toState:       store.RequestStateApproved,
		allowedFroms:  []store.RequestState{store.RequestStateNeedsReview},
		requireReason: false,
	})
}

// apiDenyRequest handles POST /api/v1/requests/{id}/deny.
//
// Body: {"reason": "..."} — required (denials without a stated
// reason are hostile to the requester and useless to auditors).
//
// Legal from-states: pending, preflighting, needs_review. Approved
// and terminal states reject with 409 — once approved + fetching has
// started, the right operation is rollback, not "deny."
//
// Gate: `deny_request`.
func (h *requestHandlers) apiDenyRequest(w http.ResponseWriter, r *http.Request) {
	h.transitionByReviewer(w, r, transitionConfig{
		action:    "deny_request",
		auditKind: "request_denied",
		toState:   store.RequestStateDenied,
		allowedFroms: []store.RequestState{
			store.RequestStatePending,
			store.RequestStatePreflighting,
			store.RequestStateNeedsReview,
		},
		requireReason: true,
	})
}

// transitionConfig wraps the per-endpoint variation; the bulk of
// the approve/deny flow is shared (gate, look up, check state,
// transition, audit).
type transitionConfig struct {
	action        string             // policy action name
	auditKind     string             // audit_events.kind on success
	toState       store.RequestState // target state
	allowedFroms  []store.RequestState
	requireReason bool
}

// transitionByReviewer runs the shared logic for approve + deny.
// Both endpoints follow the same shape — gate, look up the row,
// verify state, optionally read reason, transition + audit.
func (h *requestHandlers) transitionByReviewer(w http.ResponseWriter, r *http.Request, cfg transitionConfig) {
	id, _ := auth.FromContext(r.Context())
	if !h.policy().Allow(id, cfg.action) {
		denyByPolicy(w, cfg.action)
		return
	}

	reqID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || reqID <= 0 {
		http.Error(w, "bad request: id must be a positive integer", http.StatusBadRequest)
		return
	}

	var reason string
	if cfg.requireReason {
		var body struct {
			Reason string `json:"reason"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		reason = strings.TrimSpace(body.Reason)
		if reason == "" {
			http.Error(w, "bad request: reason is required", http.StatusBadRequest)
			return
		}
	}

	ctx := r.Context()
	current, err := h.store.GetRequest(ctx, reqID)
	if err != nil {
		if errors.Is(err, store.ErrRequestNotFound) {
			http.Error(w, "request not found", http.StatusNotFound)
			return
		}
		h.log.Error(cfg.action+": GetRequest failed", "err", err, "id", reqID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Verify from-state is in the allowed set BEFORE we attempt the
	// CAS — gives a clean 409 without consulting the SQL CAS path
	// for the obvious "wrong state" case. The CAS still guards the
	// race where state changes between our Get and our Transition.
	if !slices.Contains(cfg.allowedFroms, current.State) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":            "request is in state " + string(current.State) + "; " + cfg.action + " not allowed",
			"current_state":    current.State,
			"allowed_from":     cfg.allowedFroms,
		})
		return
	}

	fields := &store.RequestFields{}
	if reason != "" {
		fields.DenialReason = reason
	}
	if err := h.store.TransitionRequest(ctx, reqID, current.State, cfg.toState, fields); err != nil {
		if errors.Is(err, store.ErrStateMismatch) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "concurrent modification; re-fetch and retry",
			})
			return
		}
		if errors.Is(err, store.ErrIllegalTransition) {
			// Shouldn't happen — we pre-checked allowed-froms. Defense.
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "illegal transition",
			})
			return
		}
		h.log.Error(cfg.action+": transition failed", "err", err, "id", reqID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	payload := map[string]any{"request_id": reqID, "from_state": current.State}
	if reason != "" {
		payload["reason"] = reason
	}
	if err := h.store.RecordAudit(ctx, store.AuditEvent{
		Kind:    cfg.auditKind,
		Source:  sourceTag(r, "rest"),
		Module:  current.Module,
		Version: current.Version,
		OK:      true,
		UserID:  id.DisplayName(),
		Payload: mustMarshal(payload),
	}); err != nil {
		h.log.Warn(cfg.action+": audit write failed (transition still committed)",
			"err", err, "id", reqID)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":    reqID,
		"state": cfg.toState,
	})
}

// apiPolicyEffective handles GET /api/v1/policy/effective.
//
// Returns the per-caller view of every action gate as a
// {action → bool} map, the resolved profile, AND the caller's
// resolved identity. UI uses this on every page load to:
//   1. decide button visibility (actions map)
//   2. render "signed in as X" affordances (identity field)
//
// The identity field works for every auth source — bearer
// (Authorization header), header (X-Forwarded-* from a trusted
// reverse proxy), or anonymous (source="anonymous", other fields
// empty). The UI's AuthButton uses this to detect header-auth
// without needing a token in localStorage.
//
// No gate on this endpoint itself — the RESPONSE describes the
// caller's permissions, so it can't be gated by those permissions.
//
// Per-target gates (maintain_module) always report false here —
// the global view can't know the target. The UI calls AllowFor on
// the specific page where the target is known.
func (h *requestHandlers) apiPolicyEffective(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	pol := h.policy()
	actions := make(map[string]bool, len(pol.Auth.Actions))
	for name := range pol.Auth.Actions {
		actions[name] = pol.Allow(id, name)
	}
	source := string(id.Source)
	if source == "" {
		source = string(auth.SourceAnonymous)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"profile": pol.Profile,
		"actions": actions,
		"identity": map[string]any{
			"email":  id.Email,
			"user":   id.User,
			"groups": id.Groups,
			"source": source,
		},
	})
}

// denyByPolicy writes the canonical 403 response for a policy-gate
// denial. The error string deliberately doesn't distinguish
// authenticated-vs-group-vs-deny so probing can't enumerate the
// gate's shape.
func denyByPolicy(w http.ResponseWriter, action string) {
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": action + " denied by policy",
	})
}

// submitterSub picks the canonical-id field for the audit trail.
// Prefers email (stable across SSO providers), then user, then
// "anonymous" so the column is never empty.
func submitterSub(id auth.Identity) string {
	if id.Email != "" {
		return id.Email
	}
	if id.User != "" {
		return id.User
	}
	return "anonymous"
}

// mustMarshal returns the JSON encoding of v or panics. Used for
// audit-event payloads built from in-process map[string]any —
// failure here is a programmer bug, not a runtime condition.
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("audit payload marshal failed: %v (value=%#v)", err, v))
	}
	return b
}
