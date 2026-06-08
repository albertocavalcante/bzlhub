// Maintainer grant / revoke / list endpoints. Wires the existing
// module_maintainers store + policy `grant_maintainer` / `view_maintainers`
// gates into the procurement HTTP surface.
//
// Today these endpoints have no canopy-internal consumer — the
// `maintain_module` policy gate is reachable via the Evaluator
// from chunk 6 but no handler invokes it yet. Shipping the
// grant/revoke wire lets operators script the population step
// (and unblocks a future maintainer-admin UI).

package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/albertocavalcante/bzlhub/internal/auth"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// apiGrantMaintainer handles POST /api/v1/modules/{module}/maintainers.
//
// Body: {"email": "..."} — required.
// Gate: `grant_maintainer` (group:approver in strict).
// Idempotent — re-granting the same (module, email) is a no-op
// (store-level INSERT OR IGNORE).
func (h *requestHandlers) apiGrantMaintainer(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if !h.policy().Allow(id, "grant_maintainer") {
		denyByPolicy(w, "grant_maintainer")
		return
	}
	module := strings.TrimSpace(chi.URLParam(r, "module"))
	if module == "" {
		http.Error(w, "bad request: module path required", http.StatusBadRequest)
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(body.Email)
	if email == "" {
		http.Error(w, "bad request: email required", http.StatusBadRequest)
		return
	}
	grantedBy := id.DisplayName()
	if grantedBy == "" {
		grantedBy = "anonymous"
	}
	if err := h.store.AddMaintainer(r.Context(), module, email, grantedBy); err != nil {
		h.log.Error("grant_maintainer: AddMaintainer failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.recordMaintainerAudit(r, id, "maintainer_granted", module, email)
	writeJSON(w, http.StatusOK, map[string]any{
		"module": module,
		"email":  email,
	})
}

// apiRevokeMaintainer handles DELETE /api/v1/modules/{module}/maintainers/{email}.
//
// Gate: `grant_maintainer` (revoke is the same authority as grant).
// Idempotent — deleting a non-existent grant returns 200.
func (h *requestHandlers) apiRevokeMaintainer(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if !h.policy().Allow(id, "grant_maintainer") {
		denyByPolicy(w, "grant_maintainer")
		return
	}
	module := strings.TrimSpace(chi.URLParam(r, "module"))
	email := strings.TrimSpace(chi.URLParam(r, "email"))
	if module == "" || email == "" {
		http.Error(w, "bad request: module + email required", http.StatusBadRequest)
		return
	}
	if err := h.store.RemoveMaintainer(r.Context(), module, email); err != nil {
		h.log.Error("revoke_maintainer: RemoveMaintainer failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.recordMaintainerAudit(r, id, "maintainer_revoked", module, email)
	writeJSON(w, http.StatusOK, map[string]any{
		"module": module,
		"email":  email,
	})
}

// apiListMaintainers handles GET /api/v1/modules/{module}/maintainers.
//
// Gate: `view_maintainers` (any in strict — public).
// Returns {"maintainers": []} (never null) for empty.
func (h *requestHandlers) apiListMaintainers(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if !h.policy().Allow(id, "view_maintainers") {
		denyByPolicy(w, "view_maintainers")
		return
	}
	module := strings.TrimSpace(chi.URLParam(r, "module"))
	if module == "" {
		http.Error(w, "bad request: module path required", http.StatusBadRequest)
		return
	}
	rows, err := h.store.ListMaintainers(r.Context(), module)
	if err != nil {
		h.log.Error("list_maintainers: store failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []store.Maintainer{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"maintainers": rows})
}

// recordMaintainerAudit centralizes the audit-write for both
// grant and revoke. Failures are logged but don't fail the
// request (the store-side transition already committed).
func (h *requestHandlers) recordMaintainerAudit(r *http.Request, id auth.Identity, kind, module, email string) {
	err := h.store.RecordAudit(r.Context(), store.AuditEvent{
		Kind:    kind,
		Source:  sourceTag(r, "rest"),
		Module:  module,
		OK:      true,
		UserID:  id.DisplayName(),
		Payload: mustMarshal(map[string]any{"email": email}),
	})
	if err != nil {
		h.log.Warn(kind+": audit write failed (action still committed)",
			"err", err, "module", module, "email", email)
	}
}

// sentinel to keep imports honest if the file's surface ever
// shrinks past the use of errors.
var _ = errors.Is
