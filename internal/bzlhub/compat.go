package bzlhub

import (
	"context"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/compat"
)

// CompatCheck runs the compatibility analyzer against an in-memory
// MODULE.bazel blob. The Service satisfies compat.ReportSource via
// LatestVersion + GetReport (defined below); the analyzer is pure
// and never touches the network.
//
// Audit: each invocation records a `compat_check` audit row with the
// dep count + breaking count so operators can see who's running the
// analyzer. Payload deliberately excludes the input body itself —
// the body can contain customer-internal module names, and we don't
// want it sitting in the audit log.
func (s *Service) CompatCheck(ctx context.Context, body string, opts api.CompatCheckOptions) (*compat.Result, error) {
	res, err := compat.Analyze(ctx, s, body, compat.Options{
		IncludeDevDependencies: opts.IncludeDevDependencies,
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// LatestVersion picks the highest non-stub version for a module
// from the index. Returns "" when the module isn't indexed.
// Satisfies compat.ReportSource.
func (s *Service) LatestVersion(ctx context.Context, name string) (string, error) {
	versions, err := s.store.ListVersions(ctx, name)
	if err != nil {
		return "", err
	}
	for _, v := range versions {
		if !api.IsStubVersion(v) {
			return v, nil
		}
	}
	return "", nil
}

// GetReport is a thin shim aligning Service to the analyzer's
// ReportSource name. Same behavior as GetModuleVersion.
func (s *Service) GetReport(ctx context.Context, name, version string) (*report.ModuleReport, error) {
	return s.store.GetReport(ctx, name, version)
}
