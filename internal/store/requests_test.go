package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleRequest() Request {
	return Request{
		SubmitterSub:   "alice@example.com",
		SubmitterEmail: "alice@example.com",
		AuthMethod:     "bearer",
		Module:         "rules_python",
		Version:        "1.5.0",
		SourceURL:      "https://github.com/bazelbuild/rules_python/archive/1.5.0.tar.gz",
		SubmitterNotes: "needed for new build target",
	}
}

func TestCreateRequest_HappyPath(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateRequest(context.Background(), sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Error("CreateRequest returned id=0")
	}

	got, err := s.GetRequest(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Module != "rules_python" {
		t.Errorf("module = %q", got.Module)
	}
	if got.State != RequestStatePending {
		t.Errorf("state = %q, want pending (new requests always start pending)", got.State)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt not set")
	}
	if got.StateChangedAt.IsZero() {
		t.Error("StateChangedAt not set on create")
	}
}

func TestCreateRequest_RequiresModuleAndVersion(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		name string
		mut  func(*Request)
	}{
		{"empty module", func(r *Request) { r.Module = "" }},
		{"empty version", func(r *Request) { r.Version = "" }},
		{"empty submitter sub", func(r *Request) { r.SubmitterSub = "" }},
		{"empty auth method", func(r *Request) { r.AuthMethod = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := sampleRequest()
			c.mut(&req)
			_, err := s.CreateRequest(context.Background(), req)
			if err == nil {
				t.Fatal("want error for missing required field")
			}
		})
	}
}

func TestGetRequest_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetRequest(context.Background(), 999)
	if !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("err = %v, want ErrRequestNotFound", err)
	}
}

func TestListRequests_FilterByState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id1, _ := s.CreateRequest(ctx, sampleRequest())
	req2 := sampleRequest()
	req2.Module = "rules_go"
	id2, _ := s.CreateRequest(ctx, req2)

	// Move one to approved.
	if err := s.TransitionRequest(ctx, id1, RequestStatePending, RequestStatePreflighting, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionRequest(ctx, id1, RequestStatePreflighting, RequestStateAutoPass, nil); err != nil {
		t.Fatal(err)
	}

	pending, err := s.ListRequests(ctx, RequestQuery{States: []RequestState{RequestStatePending}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != id2 {
		t.Errorf("pending got %d rows, want id=%d", len(pending), id2)
	}

	autoPass, err := s.ListRequests(ctx, RequestQuery{States: []RequestState{RequestStateAutoPass}})
	if err != nil {
		t.Fatal(err)
	}
	if len(autoPass) != 1 || autoPass[0].ID != id1 {
		t.Errorf("auto_pass got %d rows", len(autoPass))
	}
}

func TestListRequests_FilterBySubmitter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _ = s.CreateRequest(ctx, sampleRequest())
	req2 := sampleRequest()
	req2.SubmitterSub = "bob@example.com"
	_, _ = s.CreateRequest(ctx, req2)

	alice, err := s.ListRequests(ctx, RequestQuery{Submitter: "alice@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(alice) != 1 || alice[0].SubmitterSub != "alice@example.com" {
		t.Errorf("alice rows = %d", len(alice))
	}
}

func TestListRequests_Pagination(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i := range 5 {
		req := sampleRequest()
		req.Version = "1.0." + string(rune('0'+i))
		_, _ = s.CreateRequest(ctx, req)
	}
	page, err := s.ListRequests(ctx, RequestQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 {
		t.Errorf("Limit=2 returned %d rows", len(page))
	}
}

func TestTransitionRequest_LegalTransition(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.CreateRequest(ctx, sampleRequest())

	err := s.TransitionRequest(ctx, id, RequestStatePending, RequestStatePreflighting, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRequest(ctx, id)
	if got.State != RequestStatePreflighting {
		t.Errorf("state = %q", got.State)
	}
}

func TestTransitionRequest_RejectsIllegalTransition(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.CreateRequest(ctx, sampleRequest())

	// pending → indexed is illegal (skip preflight/approval)
	err := s.TransitionRequest(ctx, id, RequestStatePending, RequestStateIndexed, nil)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("err = %v, want ErrIllegalTransition", err)
	}
}

func TestTransitionRequest_StateMismatchReturnsConflict(t *testing.T) {
	// Optimistic-concurrency guard. If two workers both think the
	// request is `pending` and both try to move it to `preflighting`,
	// the second loses with ErrStateMismatch (the CAS in SQL failed).
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.CreateRequest(ctx, sampleRequest())

	// First worker wins.
	if err := s.TransitionRequest(ctx, id, RequestStatePending, RequestStatePreflighting, nil); err != nil {
		t.Fatal(err)
	}
	// Second worker tries from a stale view.
	err := s.TransitionRequest(ctx, id, RequestStatePending, RequestStatePreflighting, nil)
	if !errors.Is(err, ErrStateMismatch) {
		t.Errorf("err = %v, want ErrStateMismatch", err)
	}
}

func TestTransitionRequest_AppliesFieldUpdates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.CreateRequest(ctx, sampleRequest())

	preflight := []byte(`{"license":"Apache-2.0","verdict":"pass"}`)
	err := s.TransitionRequest(ctx, id, RequestStatePending, RequestStatePreflighting, &RequestFields{
		PreflightJSON: preflight,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRequest(ctx, id)
	if string(got.PreflightJSON) != string(preflight) {
		t.Errorf("preflight_json = %s", got.PreflightJSON)
	}
}

func TestTransitionRequest_DenialReasonPersists(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.CreateRequest(ctx, sampleRequest())
	_ = s.TransitionRequest(ctx, id, RequestStatePending, RequestStatePreflighting, nil)

	err := s.TransitionRequest(ctx, id, RequestStatePreflighting, RequestStateDenied, &RequestFields{
		DenialReason: "license unknown and operator denied",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRequest(ctx, id)
	if got.DenialReason == "" {
		t.Error("denial_reason not persisted")
	}
}

func TestRequestState_CanTransitionTo(t *testing.T) {
	// Pure state machine table — pins Plan 67's transition graph.
	legal := []struct {
		from, to RequestState
	}{
		{RequestStatePending, RequestStatePreflighting},
		{RequestStatePending, RequestStateDenied}, // manual early-deny escape hatch
		{RequestStatePreflighting, RequestStateAutoPass},
		{RequestStatePreflighting, RequestStateNeedsReview},
		{RequestStatePreflighting, RequestStateDenied},
		{RequestStateNeedsReview, RequestStateApproved},
		{RequestStateNeedsReview, RequestStateDenied},
		{RequestStateAutoPass, RequestStateFetching},
		{RequestStateApproved, RequestStateFetching},
		{RequestStateFetching, RequestStateIndexed},
		{RequestStateFetching, RequestStateDenied},
	}
	for _, c := range legal {
		if !c.from.CanTransitionTo(c.to) {
			t.Errorf("legal %s→%s rejected", c.from, c.to)
		}
	}

	illegal := []struct {
		from, to RequestState
	}{
		{RequestStatePending, RequestStateIndexed},   // skip everything
		// pending → denied is now LEGAL (escape hatch) — removed from
		// this list. See legalTransitions comment.
		{RequestStatePending, RequestStateApproved},  // skip preflight
		{RequestStateIndexed, RequestStatePending},   // terminal
		{RequestStateDenied, RequestStatePending},    // terminal
		{RequestStateApproved, RequestStateIndexed},  // skip fetching
		{RequestStateAutoPass, RequestStatePending},  // backwards
	}
	for _, c := range illegal {
		if c.from.CanTransitionTo(c.to) {
			t.Errorf("illegal %s→%s accepted", c.from, c.to)
		}
	}
}

func TestFindOpenRequest_ReturnsOpen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.CreateRequest(ctx, sampleRequest())
	got, err := s.FindOpenRequest(ctx, "rules_python", "1.5.0")
	if err != nil {
		t.Fatalf("FindOpenRequest: %v", err)
	}
	if got.ID != id {
		t.Errorf("found id=%d, want %d", got.ID, id)
	}
}

func TestFindOpenRequest_IgnoresTerminal(t *testing.T) {
	// indexed + denied are terminal — FindOpenRequest must skip them
	// so a re-submission after a terminal verdict starts a NEW request.
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.CreateRequest(ctx, sampleRequest())
	// Drive the request to denied.
	_ = s.TransitionRequest(ctx, id, RequestStatePending, RequestStatePreflighting, nil)
	_ = s.TransitionRequest(ctx, id, RequestStatePreflighting, RequestStateDenied, &RequestFields{DenialReason: "test"})

	_, err := s.FindOpenRequest(ctx, "rules_python", "1.5.0")
	if !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("err = %v, want ErrRequestNotFound (denied is terminal)", err)
	}
}

func TestFindOpenRequest_ReturnsNewestWhenMultiple(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id1, _ := s.CreateRequest(ctx, sampleRequest())
	// Drive id1 through preflight to indexed (terminal — shouldn't match).
	_ = s.TransitionRequest(ctx, id1, RequestStatePending, RequestStatePreflighting, nil)
	_ = s.TransitionRequest(ctx, id1, RequestStatePreflighting, RequestStateAutoPass, nil)
	_ = s.TransitionRequest(ctx, id1, RequestStateAutoPass, RequestStateFetching, nil)
	_ = s.TransitionRequest(ctx, id1, RequestStateFetching, RequestStateIndexed, nil)

	id2, _ := s.CreateRequest(ctx, sampleRequest())
	got, err := s.FindOpenRequest(ctx, "rules_python", "1.5.0")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id2 {
		t.Errorf("found id=%d, want id2=%d (skip terminal id1=%d)", got.ID, id2, id1)
	}
}

func TestAnyRequestFor_None(t *testing.T) {
	s := newTestStore(t)
	ok, err := s.AnyRequestFor(context.Background(), "rules_python", "1.5.0")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("should be false when no request exists")
	}
}

func TestAnyRequestFor_IncludesTerminal(t *testing.T) {
	// Unlike FindOpenRequest, this probe matches terminal states
	// too — used by `bzlhub seed` to make re-runs idempotent even
	// after past denials/indexings.
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.CreateRequest(ctx, sampleRequest())
	_ = s.TransitionRequest(ctx, id, RequestStatePending, RequestStatePreflighting, nil)
	_ = s.TransitionRequest(ctx, id, RequestStatePreflighting, RequestStateDenied, &RequestFields{DenialReason: "x"})

	ok, _ := s.AnyRequestFor(ctx, "rules_python", "1.5.0")
	if !ok {
		t.Error("denied request should still count for AnyRequestFor")
	}
}

func TestRequestState_Terminal(t *testing.T) {
	if !RequestStateIndexed.IsTerminal() {
		t.Error("indexed must be terminal")
	}
	if !RequestStateDenied.IsTerminal() {
		t.Error("denied must be terminal")
	}
	if RequestStatePending.IsTerminal() {
		t.Error("pending must NOT be terminal")
	}
}

// TestReclaimStuckFetching covers the crash-mid-retry recovery path
// (Plan 76 §2.3). A request that entered `fetching` and never reached
// a terminal — because the worker process died — must be resettable to
// `approved` on a future boot so the admit loop picks it back up.
func TestReclaimStuckFetching_RecoversStuckRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, err := s.CreateRequest(ctx, sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to RequestState }{
		{RequestStatePending, RequestStatePreflighting},
		{RequestStatePreflighting, RequestStateAutoPass},
		{RequestStateAutoPass, RequestStateFetching},
	} {
		if err := s.TransitionRequest(ctx, id, step.from, step.to, nil); err != nil {
			t.Fatalf("setup %s→%s: %v", step.from, step.to, err)
		}
	}

	// Anything with state_changed_at < far-future timestamp matches.
	n, err := s.ReclaimStuckFetching(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reclaim count=%d, want 1", n)
	}

	got, err := s.GetRequest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != RequestStateApproved {
		t.Errorf("state=%s after reclaim, want approved", got.State)
	}
}

// TestReclaimStuckFetching_RespectsThreshold confirms that fresh
// `fetching` rows (newer than the threshold) are NOT reclaimed —
// otherwise the sweeper would race active workers.
func TestReclaimStuckFetching_RespectsThreshold(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, err := s.CreateRequest(ctx, sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to RequestState }{
		{RequestStatePending, RequestStatePreflighting},
		{RequestStatePreflighting, RequestStateAutoPass},
		{RequestStateAutoPass, RequestStateFetching},
	} {
		if err := s.TransitionRequest(ctx, id, step.from, step.to, nil); err != nil {
			t.Fatal(err)
		}
	}

	// Threshold in the past — row is too fresh to reclaim.
	n, err := s.ReclaimStuckFetching(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("reclaim count=%d, want 0 (row too fresh)", n)
	}

	got, _ := s.GetRequest(ctx, id)
	if got.State != RequestStateFetching {
		t.Errorf("state=%s, want fetching (unchanged)", got.State)
	}
}

// TestReclaimStuckFetching_OnlyFetching confirms rows in other states
// are never touched — only `fetching` is in scope for sweep recovery.
func TestReclaimStuckFetching_OnlyFetching(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, err := s.CreateRequest(ctx, sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	// Leave it in pending.
	n, err := s.ReclaimStuckFetching(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("reclaim count=%d, want 0 (pending is out of scope)", n)
	}
	got, _ := s.GetRequest(ctx, id)
	if got.State != RequestStatePending {
		t.Errorf("state=%s, want pending (unchanged)", got.State)
	}
}
