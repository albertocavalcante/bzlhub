package bcrmirror

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// MirrorStats is the operator-facing summary of a Mirror's on-disk
// state. Returned by Mirror.Stats; intended for /api/system/status
// surfaces, CLI `bzlhub status`, and dashboard tiles.
type MirrorStats struct {
	// LastSyncAt is the wall-clock time of the most recent successful
	// Sync (or Clone, for an unsynced mirror). Zero value when the
	// LAST_SYNC marker is missing or unreadable — pair with
	// LastSyncReadErr to surface "never synced" vs "marker corrupted".
	LastSyncAt time.Time

	// ModuleCount is the number of folders directly under modules/.
	// Counts the module names regardless of how many versions each has.
	ModuleCount int

	// SizeBytes is the total on-disk footprint of the mirror directory
	// in bytes, INCLUDING the .git tree. This is what an operator
	// would see in `du -sh <mirror>` (modulo filesystem-block rounding).
	// Skipped entries are logged-and-ignored: a transient stat failure
	// on one file shouldn't fail the entire stats call.
	SizeBytes int64
}

// Stats returns a MirrorStats snapshot. The Mirror must be Open;
// returns a wrapped ErrNoMirror otherwise.
//
// Walks the on-disk tree under Mirror.Path to compute SizeBytes;
// O(file_count) but each file is a stat call only (no read), so
// even large mirrors (BCR's ~220 MB / 6k+ commits) finish in
// well under a second on local SSD.
func (m *Mirror) Stats(ctx context.Context) (MirrorStats, error) {
	if err := ctx.Err(); err != nil {
		return MirrorStats{}, err
	}
	if m.Path == "" {
		return MirrorStats{}, fmt.Errorf("%w: Mirror not Open (empty Path)", ErrNoMirror)
	}

	mods, err := m.ListModules(ctx)
	if err != nil {
		// Module listing failure surfaces back to the caller — it's
		// a real signal ("modules/ unreadable") not a transient
		// stat blip.
		return MirrorStats{}, fmt.Errorf("bcrmirror.Stats: ListModules: %w", err)
	}

	size, err := dirSize(ctx, m.Path)
	if err != nil {
		return MirrorStats{}, fmt.Errorf("bcrmirror.Stats: dirSize: %w", err)
	}

	return MirrorStats{
		LastSyncAt:  m.LastSync(),
		ModuleCount: len(mods),
		SizeBytes:   size,
	}, nil
}

// dirSize walks root and sums regular-file sizes. Skips directories
// and unreadable entries (logged to the walk's err callback path,
// which we treat as continue — partial sums are useful diagnostics
// even with one stat failure deep in .git/).
//
// Honors ctx cancellation between entries; long mirrors don't tie up
// the caller indefinitely.
func dirSize(ctx context.Context, root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			// One stat failure mid-walk shouldn't abort the whole
			// stats call. Skip and keep going.
			if errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil // same skip-and-continue policy
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}
