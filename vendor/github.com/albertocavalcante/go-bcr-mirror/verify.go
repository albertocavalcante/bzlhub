package bcrmirror

import (
	"context"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
)

// VerifyCommit verifies the cryptographic signature on a commit
// against an allowed-signers set. In v0.1.0 this is a stub:
//
//   - When opts.RequireSignature is false (the default): VerifyCommit
//     returns nil. Callers in "unsigned commits OK" mode get a no-op.
//
//   - When opts.RequireSignature is true: VerifyCommit returns
//     ErrSignatureVerificationNotImplemented. Callers that genuinely
//     need signature verification must wait for v0.2.
//
// This shape lets canopy's bcrmirror backend ship in M2 without
// signature verification blocking it; the sync runner (Plan 21 B4)
// landing in M4 carries RequireSignature=false until v0.2 closes
// this stub.
//
// The opts.AllowedSignersFile / opts.AllowedSigners fields are
// accepted as inputs but not consulted at v0.1.0. They are
// validated for shape (non-conflicting both-set vs both-empty) so
// callers can wire the config now and have it work transparently
// when v0.2 implements verification.
//
// Mirror must be Open()ed before VerifyCommit.
//
// Full design at canopy docs/plans/51-go-bcr-mirror-v02-signature-
// verification.md.
func (m *Mirror) VerifyCommit(ctx context.Context, sha string, opts VerifyOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	repo, err := m.requireOpenRepo()
	if err != nil {
		return err
	}
	if sha == "" {
		return fmt.Errorf("bcrmirror.VerifyCommit: empty sha")
	}

	// Validate the commit SHA exists in the repo before deciding
	// stub-or-error. Operator misconfiguration (a non-existent
	// SHA) should surface even at v0.1.
	if _, err := repo.CommitObject(plumbing.NewHash(sha)); err != nil {
		return fmt.Errorf("bcrmirror.VerifyCommit: resolve commit %s: %w", sha, err)
	}

	// Shape-check the options so consumers can wire the full
	// config now without surprises in v0.2.
	if opts.AllowedSignersFile != "" && len(opts.AllowedSigners) != 0 {
		return fmt.Errorf(
			"bcrmirror.VerifyCommit: both AllowedSignersFile and AllowedSigners set; pick one")
	}

	if !opts.RequireSignature {
		// Default posture in v0.1: pass.
		return nil
	}

	return ErrSignatureVerificationNotImplemented
}
