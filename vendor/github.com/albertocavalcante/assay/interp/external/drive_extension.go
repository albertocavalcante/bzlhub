package external

import (
	"context"
	"fmt"

	bazelctx "github.com/albertocavalcante/starlark-go-bazel/ctx"
	"github.com/albertocavalcante/starlark-go-bazel/eval"
	"github.com/albertocavalcante/starlark-go-bazel/types"
)

// DriveExtensionFromSource parses the given .bzl source, locates the
// module_extension global named extName, and drives it with the
// supplied ModuleSpecs. Returns the deduplicated URL surface that
// extension would produce in a Bazel build using exactly those
// modules as the use_extension consumers.
//
// Designed for canopy's corpus-driven query path:
//   - The producer ruleset persisted its extension-impl .bzl bytes at
//     ingest (canopy/internal/store.ModuleExtensionSource).
//   - The cross-module use_extension index supplies real tag values
//     that consumers pin.
//   - This function bridges the two — same eval pipeline as Analyze
//     uses for default-tag drives, just with one specific extension
//     instead of walking a whole tree.
//
// External loads in source default to Permissive (same posture as
// Analyze), so the producer's load() statements pulling from other
// rulesets resolve without needing those rulesets' source.
func DriveExtensionFromSource(
	ctx context.Context,
	source []byte,
	filename, extName string,
	modules []bazelctx.ModuleSpec,
	opts Options,
) (*Result, error) {
	result := &Result{}

	res, err := evalBzlSource(filename, source, opts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}

	val, ok := res.Globals[extName]
	if !ok {
		return nil, fmt.Errorf("module_extension %q not found in %s", extName, filename)
	}
	ext, ok := val.(*types.ModuleExtensionClass)
	if !ok {
		return nil, fmt.Errorf("global %q in %s is %T, not *types.ModuleExtensionClass", extName, filename, val)
	}

	inv, err := eval.InvokeModuleExtension(ctx, ext, modules, eval.InvokeOptions{
		Version:   opts.BazelVersion,
		Platforms: opts.Platforms,
	})
	if err != nil {
		return nil, fmt.Errorf("invoke extension %s: %w", extName, err)
	}
	for _, u := range inv.URLs {
		result.Refs = append(result.Refs, makeRef(u, filename))
	}
	result.ForkErrors = append(result.ForkErrors, inv.ForkErrors...)
	result.Refs = dedupeRefs(result.Refs)
	return result, nil
}
