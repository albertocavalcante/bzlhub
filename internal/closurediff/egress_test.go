package closurediff

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/egress"
)

// walkClosure must resolve through the process-wide egress policy.
//
// This is a regression test for a real bypass, not a hypothetical. gobzlmod
// builds its own default client when none is supplied, so omitting
// WithHTTPClient let a closure walk reach the upstream registry with the
// egress mode ignored and nothing written to the audit sink -- and
// Service.DiffClosure is reachable over the API, so an operator-visible
// request could leave the network under mirror-only.
//
// The assertion is deliberately about DENIAL rather than success. A test that
// checked a successful fetch would pass with the policy bypassed, since
// bypassing it is precisely what makes a blocked request succeed.
func TestWalkClosure_HonoursEgressPolicy(t *testing.T) {
	// An upstream that would answer if it were ever reached. If policy is
	// bypassed the walk gets a real (if useless) response and no denial.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	sink := &recordingSink{}
	// ModeDeny with an empty allowlist: nothing may leave.
	egress.ConfigureDefault(egress.Policy{Mode: egress.ModeDeny}, sink)
	t.Cleanup(func() { egress.ConfigureDefault(egress.Policy{Mode: egress.ModeAllow}, nil) })

	_, err := walkClosure(context.Background(), "some_module", "1.0.0", upstream.URL)
	if err == nil {
		t.Fatal("walkClosure succeeded against a denied upstream; the egress policy was not applied")
	}
	if !mentionsEgress(err) && len(sink.reasons) == 0 {
		t.Errorf("walkClosure failed, but neither the error nor the audit sink shows a policy denial.\n"+
			"  A transport error would look the same, so this cannot distinguish\n"+
			"  'policy denied it' from 'the host was unreachable'.\n  err = %v", err)
	}
}

// mentionsEgress reports whether err carries the egress sentinel, directly or
// wrapped. gobzlmod wraps transport errors in its own types, so errors.Is is
// tried first and a string check is the fallback for wrapping that loses the
// chain.
func mentionsEgress(err error) bool {
	if errors.Is(err, egress.ErrEgressForbidden) {
		return true
	}
	return strings.Contains(err.Error(), egress.ErrEgressForbidden.Error())
}

type recordingSink struct{ reasons []string }

func (s *recordingSink) Emit(e egress.AuditEvent) {
	if e.Reason != "" {
		s.reasons = append(s.reasons, e.Reason)
	}
}
