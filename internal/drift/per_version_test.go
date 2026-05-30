package drift

import (
	"testing"

	"github.com/albertocavalcante/canopy/internal/fetch"
)

// TestComputeForVersion_Behind asserts the "we have an older version
// than upstream's latest" case: count of newer upstream versions
// feeds Behind, latest upstream feeds LatestUpstream, status=behind.
// The DriftChip's primary rendering trigger.
func TestComputeForVersion_Behind(t *testing.T) {
	up := &fetch.MetadataJSON{Versions: []string{"1.0.0", "1.1.0", "1.2.0"}}
	got := ComputeForVersion("1.0.0", up)

	if got.Status != VersionStatusBehind {
		t.Errorf("Status = %q; want %q", got.Status, VersionStatusBehind)
	}
	if got.Behind != 2 {
		t.Errorf("Behind = %d; want 2", got.Behind)
	}
	if got.LatestUpstream != "1.2.0" {
		t.Errorf("LatestUpstream = %q; want %q", got.LatestUpstream, "1.2.0")
	}
}

// TestComputeForVersion_InSync asserts the no-newer-upstream case:
// status=in-sync; UI renders nothing.
func TestComputeForVersion_InSync(t *testing.T) {
	up := &fetch.MetadataJSON{Versions: []string{"1.0.0", "1.1.0"}}
	got := ComputeForVersion("1.1.0", up)

	if got.Status != VersionStatusInSync {
		t.Errorf("Status = %q; want %q", got.Status, VersionStatusInSync)
	}
	if got.Behind != 0 {
		t.Errorf("Behind = %d; want 0", got.Behind)
	}
}

// TestComputeForVersion_YankedTakesPrecedence asserts the yanked-
// upstream signal beats behind: even when newer non-yanked versions
// exist, the yanked status surfaces (security signal > freshness
// signal, per Plan 19 Idea A).
func TestComputeForVersion_YankedTakesPrecedence(t *testing.T) {
	up := &fetch.MetadataJSON{
		Versions:       []string{"1.0.0", "1.1.0", "1.2.0"},
		YankedVersions: map[string]string{"1.0.0": "CVE-2026-XXXX"},
	}
	got := ComputeForVersion("1.0.0", up)

	if got.Status != VersionStatusYankedUpstream {
		t.Errorf("Status = %q; want %q", got.Status, VersionStatusYankedUpstream)
	}
}

// TestComputeForVersion_LocalOnly asserts the no-upstream-module
// case: when MetadataAt returned ErrModuleNotFound and the caller
// passed nil, every local version is local-only.
func TestComputeForVersion_LocalOnly(t *testing.T) {
	got := ComputeForVersion("1.0.0", nil)

	if got.Status != VersionStatusLocalOnly {
		t.Errorf("Status = %q; want %q", got.Status, VersionStatusLocalOnly)
	}
	if got.LatestUpstream != "" {
		t.Errorf("LatestUpstream = %q; want empty (no upstream)", got.LatestUpstream)
	}
}

// TestComputeForVersion_LocalVersionAheadOfUpstream covers the
// "canopy holds a version upstream doesn't have, and it's the highest
// overall" case (canopy's own published variant). Treated as in-sync
// — we're not behind; we're ahead.
func TestComputeForVersion_LocalVersionAheadOfUpstream(t *testing.T) {
	up := &fetch.MetadataJSON{Versions: []string{"1.0.0", "1.1.0"}}
	got := ComputeForVersion("2.0.0", up)

	if got.Status != VersionStatusInSync {
		t.Errorf("Status = %q; want %q (local ahead of upstream is in-sync)", got.Status, VersionStatusInSync)
	}
}

// TestComputeForVersion_EmptyUpstreamVersions asserts the edge case
// of an upstream module with zero versions (corrupt metadata or
// brand-new module): treated as in-sync rather than crashing —
// pickLatest returns "" and behind count stays 0.
func TestComputeForVersion_EmptyUpstreamVersions(t *testing.T) {
	up := &fetch.MetadataJSON{Versions: nil}
	got := ComputeForVersion("1.0.0", up)

	if got.Status != VersionStatusInSync {
		t.Errorf("Status = %q; want %q (no upstream versions)", got.Status, VersionStatusInSync)
	}
}
