package bcrmirror

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// lastSyncFileName is the on-disk record under <mirror>/.git/.
// ALL_CAPS matches git's convention for its own state files (HEAD,
// FETCH_HEAD, etc.).
const lastSyncFileName = "LAST_SYNC"

// lastSyncRecord is the on-disk shape of LAST_SYNC.
type lastSyncRecord struct {
	SHA  string    `json:"sha"`
	Time time.Time `json:"time"`
}

// writeLastSyncFile persists lastSync state for cross-process
// consumers via tmp + atomic rename. Best-effort: write failures
// are silent (in-memory state is authoritative for this process).
func writeLastSyncFile(mirrorPath, sha string, when time.Time) {
	if mirrorPath == "" {
		return
	}
	dir := filepath.Join(mirrorPath, ".git")
	if _, err := os.Stat(dir); err != nil {
		return
	}
	data, _ := json.Marshal(lastSyncRecord{SHA: sha, Time: when})

	target := filepath.Join(dir, lastSyncFileName)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, target)
}

// readLastSyncFile loads LAST_SYNC. Returns (zero, nil) when the
// file is absent; returns (zero, err) when the file exists but
// can't be read or parsed. Callers fall back to a zero time in
// either case and SHOULD log the error path so a hand-edited or
// truncated file doesn't fail silently.
func readLastSyncFile(mirrorPath string) (time.Time, error) {
	path := filepath.Join(mirrorPath, ".git", lastSyncFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	var rec lastSyncRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return time.Time{}, fmt.Errorf("parse LAST_SYNC at %s: %w", path, err)
	}
	return rec.Time, nil
}
