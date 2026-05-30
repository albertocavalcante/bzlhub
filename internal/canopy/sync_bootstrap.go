package canopy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"

	"github.com/albertocavalcante/canopy/internal/store"
)

// ErrAlreadyBootstrapped is returned by SyncBootstrap when the
// target MirrorPath already contains a clone of Remote. Callers
// (the `canopy sync bootstrap` CLI) inspect with errors.Is and
// either skip silently (idempotent re-runs) or surface the
// "use --reinit" hint to the operator. The bcrmirror library
// returns ErrAlreadyCloned for the same condition; canopy wraps it
// in a canopy-flavoured sentinel so consumers don't need to import
// the upstream package just to inspect the error.
var ErrAlreadyBootstrapped = errors.New("canopy: mirror already bootstrapped (pass Reinit=true to wipe + reclone)")

// SyncBootstrapOptions controls a single bootstrap call.
type SyncBootstrapOptions struct {
	// Remote is the upstream BCR git URL. Required.
	Remote string

	// MirrorPath is the on-disk directory the clone lands in.
	// Required. Created if absent.
	MirrorPath string

	// Branch is the upstream branch to clone. Defaults to "main"
	// (bcrmirror.CloneOptions's default) when empty.
	Branch string

	// Reinit, when true, deletes any existing on-disk clone at
	// MirrorPath and clones fresh. Without this flag, an existing
	// clone returns ErrAlreadyBootstrapped untouched.
	//
	// Destructive: os.RemoveAll(MirrorPath). The CLI exposes this
	// behind a --reinit flag so the operator's intent is explicit.
	Reinit bool
}

// SyncBootstrapReceipt summarises the bootstrap. Populated even on
// the Reinit path (with the post-wipe clone's SHA).
type SyncBootstrapReceipt struct {
	SHA      string
	Bytes    int64
	Duration time.Duration
}

// SyncBootstrap performs the initial clone of a BCR-shape upstream
// into MirrorPath and records one audit_events row.
//
// Behaviour:
//
//   - Empty Remote or empty MirrorPath → explicit error, no audit
//     (operator misconfiguration; nothing to record beyond the CLI
//     parse failure).
//   - Reinit=false + existing valid clone → ErrAlreadyBootstrapped,
//     no audit (the bootstrap is a no-op; nothing happened to
//     record).
//   - Reinit=true + existing clone → wipe + reclone; one
//     "sync_bootstrap_success" audit on success, one
//     "sync_bootstrap_failure" on error.
//   - Clean clone → one audit row.
//
// The Service does NOT attach the resulting Mirror to itself —
// SyncBootstrap is a one-shot side-effect, not a lifecycle setup.
// The next `canopy serve --root <MirrorPath>` invocation picks up
// the new clone via backend.NewFromRoot's auto-detect.
//
// Plan trail: Plan 20 §sync-bootstrap; Plan 21 B4 adds the
// `canopy sync run` companion verb for periodic refresh.
func (s *Service) SyncBootstrap(ctx context.Context, opts SyncBootstrapOptions) (SyncBootstrapReceipt, error) {
	var rec SyncBootstrapReceipt
	if opts.Remote == "" {
		return rec, errors.New("canopy.SyncBootstrap: empty Remote")
	}
	if opts.MirrorPath == "" {
		return rec, errors.New("canopy.SyncBootstrap: empty MirrorPath")
	}

	if opts.Reinit {
		if err := os.RemoveAll(opts.MirrorPath); err != nil {
			return rec, fmt.Errorf("canopy.SyncBootstrap: reinit wipe %s: %w", opts.MirrorPath, err)
		}
	}

	start := time.Now()
	m := bcrmirror.New(opts.MirrorPath, opts.Remote)
	cr, err := m.Clone(ctx, bcrmirror.CloneOptions{Branch: opts.Branch})
	if err != nil {
		if errors.Is(err, bcrmirror.ErrAlreadyCloned) {
			// Don't audit — nothing changed on disk. The caller's
			// CLI surfaces the "use --reinit" hint.
			return SyncBootstrapReceipt{SHA: cr.SHA}, fmt.Errorf("%w (HEAD=%s)", ErrAlreadyBootstrapped, cr.SHA)
		}
		s.recordBootstrapAudit(ctx, opts, start, "", err)
		return rec, fmt.Errorf("canopy.SyncBootstrap: clone %s: %w", opts.Remote, err)
	}

	rec.SHA = cr.SHA
	rec.Bytes = cr.Bytes
	rec.Duration = time.Since(start)

	s.recordBootstrapAudit(ctx, opts, start, rec.SHA, nil)
	return rec, nil
}

// recordBootstrapAudit writes one audit row capturing the bootstrap
// outcome. Payload carries the remote URL, on-disk path, post-clone
// SHA (on success), and the reinit flag — enough for an operator
// reading /api/history to reconstruct what happened.
func (s *Service) recordBootstrapAudit(ctx context.Context, opts SyncBootstrapOptions, start time.Time, sha string, opErr error) {
	kind := "sync_bootstrap_success"
	ok := true
	errMsg := ""
	if opErr != nil {
		kind = "sync_bootstrap_failure"
		ok = false
		errMsg = opErr.Error()
	}
	payload, mErr := json.Marshal(struct {
		Remote     string `json:"remote"`
		MirrorPath string `json:"mirror_path"`
		Branch     string `json:"branch,omitempty"`
		SHA        string `json:"sha,omitempty"`
		Reinit     bool   `json:"reinit,omitempty"`
	}{
		Remote:     opts.Remote,
		MirrorPath: opts.MirrorPath,
		Branch:     opts.Branch,
		SHA:        sha,
		Reinit:     opts.Reinit,
	})
	if mErr != nil {
		slog.Warn("sync_bootstrap audit payload marshal failed", "err", mErr)
		payload = nil
	}
	s.audit(ctx, store.AuditEvent{
		Kind:       kind,
		Source:     "cli",
		OK:         ok,
		DurationMs: time.Since(start).Milliseconds(),
		Error:      errMsg,
		Payload:    payload,
	})
}
