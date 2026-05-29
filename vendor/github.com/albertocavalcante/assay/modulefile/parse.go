// Package modulefile parses MODULE.bazel content into assay's report types.
// Wraps go-bzlmod so callers don't need to know about its internal shape.
package modulefile

import (
	"fmt"
	"os"

	gobzlmod "github.com/albertocavalcante/go-bzlmod"

	"github.com/albertocavalcante/assay/report"
)

// ParseFile reads and parses a MODULE.bazel file at the given path.
// Returns the module-level fields ready to merge into a ModuleReport.
func ParseFile(path string) (*report.ModuleReport, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read MODULE.bazel: %w", err)
	}
	return ParseContent(string(b))
}

// ParseContent parses MODULE.bazel content provided as a string.
func ParseContent(content string) (*report.ModuleReport, error) {
	info, err := gobzlmod.ParseModuleContent(content)
	if err != nil {
		return nil, fmt.Errorf("parse MODULE.bazel: %w", err)
	}
	return fromInfo(info), nil
}

func fromInfo(info *gobzlmod.ModuleInfo) *report.ModuleReport {
	r := &report.ModuleReport{
		Name:               info.Name,
		Version:            info.Version,
		CompatibilityLevel: info.CompatibilityLevel,
		BazelCompatibility: info.BazelCompatibility,
	}
	for _, d := range info.Dependencies {
		r.BazelDeps = append(r.BazelDeps, report.ModuleKey{
			Name:    d.Name,
			Version: d.Version,
		})
	}
	return r
}
