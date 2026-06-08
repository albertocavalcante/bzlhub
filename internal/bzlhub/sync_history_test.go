package bzlhub

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/albertocavalcante/bzlhub/internal/store"
)

// makeFileRemoteFor materialises a local git repo with one seed
// commit and returns its path. Used by sync_history tests as the
// upstream-side of bcrmirror.Clone via file://.
func makeFileRemoteFor(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	wt, _ := repo.Worktree()
	_, _ = wt.Add("README.md")
	_, _ = wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "tester", Email: "t@x", When: time.Now()},
	})
	return dir
}

// mustOpenMirror is the no-error sibling of bcrmirror.New + Open,
// for tests where the path is known-good (e.g. just bootstrapped).
func mustOpenMirror(t *testing.T, path string) *bcrmirror.Mirror {
	t.Helper()
	m := bcrmirror.New(path, "")
	if err := m.Open(t.Context()); err != nil {
		t.Fatalf("Mirror.Open(%s): %v", path, err)
	}
	return m
}

// TestSyncHistory_ReturnsOnlySyncKinds asserts the filter contract:
// the audit_events table carries many kinds (bump_success,
// ingest_*, etc.), but the sync history view shows ONLY the four
// sync_* kinds (bootstrap success/failure, run success/uptodate/
// failure). Without this filter the operator looking at "did my
// sync schedule run" would have to wade through unrelated events.
func TestSyncHistory_ReturnsOnlySyncKinds(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	// Seed via the real SyncBootstrap path so the audit row's
	// payload + duration shape matches production. Use a file://
	// remote so it doesn't touch the network.
	remote := makeFileRemoteFor(t)
	target := filepath.Join(t.TempDir(), "mirror")
	if _, err := svc.SyncBootstrap(ctx, SyncBootstrapOptions{
		Remote:     "file://" + remote,
		MirrorPath: target,
		Branch:     "master",
	}); err != nil {
		t.Fatalf("SyncBootstrap: %v", err)
	}

	history, err := svc.SyncHistory(ctx, 10, time.Time{})
	if err != nil {
		t.Fatalf("SyncHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d; want 1 (one bootstrap)", len(history))
	}
	if history[0].Kind != "sync_bootstrap_success" {
		t.Errorf("Kind = %q; want sync_bootstrap_success", history[0].Kind)
	}
	if !history[0].OK {
		t.Errorf("OK = false; want true")
	}
}

// TestSyncHistory_NewestFirst asserts the ordering convention
// inherited from ListAudit. Operators eyeballing the output should
// see most-recent at top — matches the audit log + activity UI.
func TestSyncHistory_NewestFirst(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	remote := makeFileRemoteFor(t)
	target := filepath.Join(t.TempDir(), "mirror")

	// First event: bootstrap.
	if _, err := svc.SyncBootstrap(ctx, SyncBootstrapOptions{
		Remote:     "file://" + remote,
		MirrorPath: target,
		Branch:     "master",
	}); err != nil {
		t.Fatalf("SyncBootstrap: %v", err)
	}

	// Second event: sync run (up-to-date since nothing changed).
	// Need to wire the Service's Mirror to the just-bootstrapped
	// clone so SyncRun has something to call against.
	mirror := mustOpenMirror(t, target)
	svc.UseMirror(mirror)
	if _, err := svc.SyncRun(ctx, SyncRunOptions{}); err != nil {
		t.Fatalf("SyncRun: %v", err)
	}

	history, err := svc.SyncHistory(ctx, 10, time.Time{})
	if err != nil {
		t.Fatalf("SyncHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history len = %d; want 2", len(history))
	}
	if history[0].Kind != "sync_run_uptodate" {
		t.Errorf("history[0].Kind = %q; want sync_run_uptodate (newest)", history[0].Kind)
	}
	if history[1].Kind != "sync_bootstrap_success" {
		t.Errorf("history[1].Kind = %q; want sync_bootstrap_success", history[1].Kind)
	}
}

// TestSyncHistory_RespectsLimit asserts the pagination contract.
// limit <= 0 should default reasonably (matches ListAudit's 100);
// limit > 0 caps the result.
func TestSyncHistory_RespectsLimit(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	remote := makeFileRemoteFor(t)
	target := filepath.Join(t.TempDir(), "mirror")
	if _, err := svc.SyncBootstrap(ctx, SyncBootstrapOptions{
		Remote:     "file://" + remote,
		MirrorPath: target,
		Branch:     "master",
	}); err != nil {
		t.Fatalf("SyncBootstrap: %v", err)
	}

	got, err := svc.SyncHistory(ctx, 0, time.Time{})
	if err != nil {
		t.Fatalf("SyncHistory(0): %v", err)
	}
	if len(got) != 1 {
		t.Errorf("limit=0 returned %d events; want 1 (default applied)", len(got))
	}
}

// TestSyncHistory_SinceFiltersOlderEvents asserts the time filter
// surfaces through to ListAudit. Operators wanting "events from
// the last hour" can pass a non-zero since; older events drop.
func TestSyncHistory_SinceFiltersOlderEvents(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	// Seed two events at known timestamps spanning a 2-hour gap.
	old := time.Now().UTC().Add(-2 * time.Hour)
	recent := time.Now().UTC().Add(-30 * time.Minute)
	for _, ts := range []time.Time{old, recent} {
		if err := svc.store.RecordAudit(ctx, store.AuditEvent{
			Timestamp: ts,
			Kind:      "sync_run_uptodate",
			Source:    "cli",
			OK:        true,
		}); err != nil {
			t.Fatalf("RecordAudit: %v", err)
		}
	}

	// Filter to the last hour — only the recent event survives.
	cutoff := time.Now().UTC().Add(-1 * time.Hour)
	got, err := svc.SyncHistory(ctx, 10, cutoff)
	if err != nil {
		t.Fatalf("SyncHistory: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("len = %d; want 1 (only the recent event)", len(got))
	}
}

// TestSyncBootstrapAuditPayload_UsesToSHA pins down the wire shape
// of the bootstrap audit payload. SyncBootstrap and SyncRun must
// agree on the JSON field name for "the HEAD SHA after this event"
// so the SyncHistory reader doesn't need a translation hack.
//
// Regression for an earlier session where SyncBootstrap emitted
// "sha" while SyncRun emitted "to_sha"; SyncHistory carried a
// fallback that quietly hid the drift.
func TestSyncBootstrapAuditPayload_UsesToSHA(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	remote := makeFileRemoteFor(t)
	target := filepath.Join(t.TempDir(), "mirror")
	if _, err := svc.SyncBootstrap(ctx, SyncBootstrapOptions{
		Remote:     "file://" + remote,
		MirrorPath: target,
		Branch:     "master",
	}); err != nil {
		t.Fatalf("SyncBootstrap: %v", err)
	}

	events, err := svc.store.ListAudit(ctx, store.AuditQuery{Limit: 1})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d audit events; want 1", len(events))
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, ok := payload["to_sha"]; !ok {
		t.Errorf("bootstrap audit payload missing to_sha (got keys %v)", keysOf(payload))
	}
	if _, ok := payload["sha"]; ok {
		t.Errorf("bootstrap audit payload still has legacy %q field (got keys %v)", "sha", keysOf(payload))
	}
}

func keysOf(m map[string]any) []string {
	return slices.Sorted(maps.Keys(m))
}

// TestSyncHistory_TolerantOfMalformedPayload asserts the reader's
// defensive contract: a row whose payload is unparseable yields
// an entry with the top-level fields populated (kind, ok, ts,
// duration) and the payload-derived fields empty. No error
// propagates — one corrupt row doesn't break the whole listing.
func TestSyncHistory_TolerantOfMalformedPayload(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	if err := svc.store.RecordAudit(ctx, store.AuditEvent{
		Kind:    "sync_run_success",
		Source:  "cli",
		OK:      true,
		Payload: []byte("not json"),
	}); err != nil {
		t.Fatalf("RecordAudit: %v", err)
	}

	history, err := svc.SyncHistory(ctx, 10, time.Time{})
	if err != nil {
		t.Fatalf("SyncHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("len = %d; want 1", len(history))
	}
	got := history[0]
	if got.Kind != "sync_run_success" || !got.OK {
		t.Errorf("top-level fields lost; got %+v", got)
	}
	if got.FromSHA != "" || got.ToSHA != "" || got.Commits != 0 {
		t.Errorf("payload-derived fields should be empty on parse failure; got %+v", got)
	}
}

// TestSyncHistory_EmptyDB asserts the empty-result contract: no
// events yet → empty slice, not nil, no error.
func TestSyncHistory_EmptyDB(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	history, err := svc.SyncHistory(ctx, 10, time.Time{})
	if err != nil {
		t.Fatalf("SyncHistory on empty DB: %v", err)
	}
	if history == nil {
		t.Errorf("history is nil; want non-nil empty slice")
	}
	if len(history) != 0 {
		t.Errorf("history len = %d; want 0", len(history))
	}
}
