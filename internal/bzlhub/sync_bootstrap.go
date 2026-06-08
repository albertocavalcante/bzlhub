package bzlhub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"
)

// ErrAlreadyBootstrapped is returned when MirrorPath already
// contains a clone of Remote.
var ErrAlreadyBootstrapped = errors.New("canopy: mirror already bootstrapped")

// SyncBootstrapOptions controls a single bootstrap call.
type SyncBootstrapOptions struct {
	Remote     string // upstream git URL (required)
	MirrorPath string // on-disk target directory (required)
	Branch     string // defaults to "main" when empty
	Reinit     bool   // when true, os.RemoveAll(MirrorPath) before cloning
}

// SyncBootstrapReceipt summarises the bootstrap.
type SyncBootstrapReceipt struct {
	SHA      string
	Bytes    int64
	Duration time.Duration
}

// SyncBootstrap performs the initial clone of a BCR-shape upstream
// into MirrorPath and records one audit row. Returns
// ErrAlreadyBootstrapped on a repeat call against the same target;
// pass Reinit=true to wipe and reclone. The Service is NOT attached
// to the resulting Mirror — the next `bzlhub serve --root` picks
// it up via backend.NewFromRoot.
func (s *Service) SyncBootstrap(ctx context.Context, opts SyncBootstrapOptions) (SyncBootstrapReceipt, error) {
	var rec SyncBootstrapReceipt
	if opts.Remote == "" {
		return rec, errors.New("bzlhub.SyncBootstrap: empty Remote")
	}
	if opts.MirrorPath == "" {
		return rec, errors.New("bzlhub.SyncBootstrap: empty MirrorPath")
	}

	if opts.Reinit {
		if err := os.RemoveAll(opts.MirrorPath); err != nil {
			return rec, fmt.Errorf("bzlhub.SyncBootstrap: reinit wipe %s: %w", opts.MirrorPath, err)
		}
	}

	start := time.Now()
	m := bcrmirror.New(opts.MirrorPath, opts.Remote)
	cr, err := m.Clone(ctx, bcrmirror.CloneOptions{Branch: opts.Branch})
	if err != nil {
		if errors.Is(err, bcrmirror.ErrAlreadyCloned) {
			// Nothing changed on disk; skip the audit row but
			// populate Duration so the receipt is consistent
			// with the success path's shape. errors.Join chains
			// both sentinels so callers can errors.Is against
			// either ErrAlreadyBootstrapped (canopy) or
			// ErrAlreadyCloned (library).
			return SyncBootstrapReceipt{
				SHA:      cr.SHA,
				Duration: time.Since(start),
			}, fmt.Errorf("%w (HEAD=%s)", errors.Join(ErrAlreadyBootstrapped, bcrmirror.ErrAlreadyCloned), cr.SHA)
		}
		s.recordBootstrapAudit(ctx, opts, start, "", err)
		return rec, fmt.Errorf("bzlhub.SyncBootstrap: clone %s: %w", opts.Remote, err)
	}

	rec.SHA = cr.SHA
	rec.Bytes = cr.Bytes
	rec.Duration = time.Since(start)

	s.recordBootstrapAudit(ctx, opts, start, rec.SHA, nil)
	return rec, nil
}

// recordBootstrapAudit writes one audit row capturing the
// bootstrap outcome.
func (s *Service) recordBootstrapAudit(ctx context.Context, opts SyncBootstrapOptions, start time.Time, sha string, opErr error) {
	kind := "sync_bootstrap_success"
	if opErr != nil {
		kind = "sync_bootstrap_failure"
	}
	s.emitSyncAudit(ctx, kind, start, opErr, syncAuditPayload{
		Remote:     opts.Remote,
		MirrorPath: opts.MirrorPath,
		Branch:     opts.Branch,
		ToSHA:      sha,
		Reinit:     opts.Reinit,
	})
}
