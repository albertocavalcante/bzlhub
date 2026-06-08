package bzlhub

import (
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/api"
)

// IsRoot must be false for every consumer-derived ModuleSpec: in the
// producer's analysis context none of its dependents is the workspace
// root. Setting IsRoot:true on an arbitrary one would mis-fire any
// extension impl that branches on `mod.is_root`.
func TestBuildModuleSpecsFromConsumers_NoRootFlag(t *testing.T) {
	specs := buildModuleSpecsFromConsumers([]api.ExtensionConsumerCall{
		{ConsumerModule: "a", ConsumerVersion: "1.0", TagName: "t",
			TagAttrs: map[string]any{"x": "first"}},
		{ConsumerModule: "b", ConsumerVersion: "2.0", TagName: "t",
			TagAttrs: map[string]any{"x": "second"}},
	})
	if len(specs) != 2 {
		t.Fatalf("specs = %d, want 2", len(specs))
	}
	for _, s := range specs {
		if s.IsRoot {
			t.Errorf("IsRoot must be false for synthesized consumer %q", s.Name)
		}
	}
}

// Multiple tag calls from the same consumer collapse into one
// ModuleSpec with multiple TagInstances under the tag name.
func TestBuildModuleSpecsFromConsumers_SameConsumerGroupsTags(t *testing.T) {
	specs := buildModuleSpecsFromConsumers([]api.ExtensionConsumerCall{
		{ConsumerModule: "myapp", ConsumerVersion: "1.0", TagName: "download",
			TagAttrs: map[string]any{"version": "1.22.5"}},
		{ConsumerModule: "myapp", ConsumerVersion: "1.0", TagName: "download",
			TagAttrs: map[string]any{"version": "1.21.3"}},
	})
	if len(specs) != 1 {
		t.Fatalf("specs = %d, want 1 (same consumer)", len(specs))
	}
	if len(specs[0].Tags["download"]) != 2 {
		t.Errorf("tags = %d, want 2 download instances", len(specs[0].Tags["download"]))
	}
}

// Different tag names on the same consumer keep separate buckets.
func TestBuildModuleSpecsFromConsumers_DifferentTagsKeepBuckets(t *testing.T) {
	specs := buildModuleSpecsFromConsumers([]api.ExtensionConsumerCall{
		{ConsumerModule: "x", ConsumerVersion: "1", TagName: "download",
			TagAttrs: map[string]any{"v": "1.0"}},
		{ConsumerModule: "x", ConsumerVersion: "1", TagName: "configure",
			TagAttrs: map[string]any{"strict": true}},
	})
	if len(specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(specs))
	}
	if len(specs[0].Tags) != 2 {
		t.Errorf("tag buckets = %d, want 2", len(specs[0].Tags))
	}
}

// toStarlarkValue coverage lives in starlark-go-bazel/conv (as
// TestFromGo_*). The converter moved out of canopy when it became
// reusable by compat-analyzer + future scip-bazel callers.
