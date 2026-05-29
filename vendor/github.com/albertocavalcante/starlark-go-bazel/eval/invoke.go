package eval

import (
	"context"
	"fmt"
	"sort"
	"time"

	bazelctx "github.com/albertocavalcante/starlark-go-bazel/ctx"
	"github.com/albertocavalcante/starlark-go-bazel/taint"
	"github.com/albertocavalcante/starlark-go-bazel/types"
	"github.com/albertocavalcante/starlark-go-bazel/version"
	"go.starlark.net/starlark"
)

const (
	// DefaultMaxSteps caps Starlark execution per Invoke call. 10M is
	// generous for production .bzl impls; pathological inputs abort fast.
	DefaultMaxSteps = 10_000_000

	// DefaultTimeout caps wall-clock per Invoke call. Not enforced today
	// (M5 ships the field but ctx-cancellation wiring is a follow-up);
	// callers should set their own context deadline.
	DefaultTimeout = 30 * time.Second
)

// InvokeOptions configures InvokeRepositoryRule and InvokeModuleExtension.
type InvokeOptions struct {
	// Version pins the Bazel surface emulated by the synthetic ctx.
	// Zero value = VLatest.
	Version version.Version

	// Platforms is the (os, arch) fork matrix for repository_rule
	// invocations. Zero/empty = single fork with empty platform.
	Platforms []taint.Platform

	// OSEnv is the process environment seen by ctx.getenv on all forks.
	OSEnv map[string]string

	// MaxSteps caps per-eval Starlark step count. Zero = DefaultMaxSteps.
	MaxSteps int64

	// Timeout caps wall-clock per Invoke. Zero = DefaultTimeout. Not
	// yet enforced; see DefaultTimeout doc.
	Timeout time.Duration
}

// InvokeResult is the per-rule output of InvokeRepositoryRule.
// URLs are deduplicated across the (os, arch) fork matrix.
type InvokeResult struct {
	URLs       []taint.CapturedURL
	ForkErrors []taint.ForkError
}

// ExtensionResult is the output of InvokeModuleExtension: the captured
// repo_rule(...) instantiations the impl declared, plus the URLs and
// errors from dispatching each through InvokeRepositoryRule.
type ExtensionResult struct {
	Instantiations []taint.RuleInstantiation
	URLs           []taint.CapturedURL
	ForkErrors     []taint.ForkError
}

// StringAttrs lifts a string-valued attr map to starlark.Value form.
// Convenience for callers whose rule has only string attrs.
func StringAttrs(m map[string]string) map[string]starlark.Value {
	out := make(map[string]starlark.Value, len(m))
	for k, v := range m {
		out[k] = starlark.String(v)
	}
	return out
}

// InvokeRepositoryRule drives `rule.Implementation` with a synthetic
// repository_ctx for each platform in opts.Platforms (or a single
// empty fork if none supplied). Captured URLs and per-fork errors
// return in the result. Per-fork eval errors are collected, not
// propagated.
func InvokeRepositoryRule(_ context.Context, rule *types.RepositoryRuleClass, attrs map[string]starlark.Value, opts InvokeOptions) (*InvokeResult, error) {
	if rule == nil {
		return nil, fmt.Errorf("invoke: rule is nil")
	}
	impl := rule.Implementation()
	if impl == nil {
		return nil, fmt.Errorf("invoke: rule %q has no implementation", rule.Name())
	}
	maxSteps := opts.MaxSteps
	if maxSteps == 0 {
		maxSteps = DefaultMaxSteps
	}
	platforms := opts.Platforms
	if len(platforms) == 0 {
		platforms = []taint.Platform{{}}
	}

	sinks := &taint.Sinks{}
	for _, plat := range platforms {
		rctx := bazelctx.NewRepositoryCtx(bazelctx.RepositoryCtxOptions{
			Name:    rule.Name(),
			OSName:  plat.OS,
			OSArch:  plat.Arch,
			OSEnv:   opts.OSEnv,
			Attrs:   attrs,
			Version: opts.Version,
			Sinks:   sinks,
		})
		thread := &starlark.Thread{
			Name: "invoke-" + plat.Label(),
			Load: emptyLoad,
		}
		thread.SetMaxExecutionSteps(uint64(maxSteps))
		if _, err := starlark.Call(thread, impl, starlark.Tuple{rctx}, nil); err != nil {
			sinks.ForkErrors = append(sinks.ForkErrors, taint.ForkError{Platform: plat, Err: err})
		}
	}

	// Stamp rule name on every captured URL that didn't get one from
	// nested dispatches.
	for i := range sinks.URLs {
		if sinks.URLs[i].RuleName == "" {
			sinks.URLs[i].RuleName = rule.Name()
		}
	}

	return &InvokeResult{
		URLs:       dedupeURLs(sinks.URLs),
		ForkErrors: sinks.ForkErrors,
	}, nil
}

// InvokeModuleExtension drives `ext.Implementation` with a synthetic
// module_ctx built from modules. repo_rule(...) calls inside the impl
// are captured into a per-thread sink, then each captured instantiation
// is dispatched through InvokeRepositoryRule with opts.Platforms.
//
// The extension impl itself runs in a single fork (linux/amd64);
// platform forking happens at the repo_rule level. Real Bazel
// matches this: extension impls are platform-agnostic, repo rules
// branch on ctx.os.
func InvokeModuleExtension(goCtx context.Context, ext *types.ModuleExtensionClass, modules []bazelctx.ModuleSpec, opts InvokeOptions) (*ExtensionResult, error) {
	if ext == nil {
		return nil, fmt.Errorf("invoke: extension is nil")
	}
	impl := ext.Implementation()
	if impl == nil {
		return nil, fmt.Errorf("invoke: extension %q has no implementation", ext.Name())
	}
	maxSteps := opts.MaxSteps
	if maxSteps == 0 {
		maxSteps = DefaultMaxSteps
	}

	instSink := &[]taint.RuleInstantiation{}
	mctx := bazelctx.NewModuleCtx(bazelctx.ModuleCtxOptions{
		Modules: modules,
		OSName:  "linux",
		OSArch:  "amd64",
		OSEnv:   opts.OSEnv,
		Version: opts.Version,
	})
	thread := &starlark.Thread{
		Name: "invoke-ext-" + ext.Name(),
		Load: emptyLoad,
	}
	thread.SetMaxExecutionSteps(uint64(maxSteps))
	thread.SetLocal(taint.InstSinkKey, instSink)

	if _, err := starlark.Call(thread, impl, starlark.Tuple{mctx}, nil); err != nil {
		return nil, fmt.Errorf("invoke extension %s: %w", ext.Name(), err)
	}

	result := &ExtensionResult{Instantiations: *instSink}
	for _, inst := range *instSink {
		rule, ok := inst.Rule.(*types.RepositoryRuleClass)
		if !ok {
			continue
		}
		inv, err := InvokeRepositoryRule(goCtx, rule, inst.Attrs, opts)
		if err != nil {
			return nil, fmt.Errorf("dispatch %s: %w", rule.Name(), err)
		}
		result.URLs = append(result.URLs, inv.URLs...)
		result.ForkErrors = append(result.ForkErrors, inv.ForkErrors...)
	}
	return result, nil
}

func emptyLoad(_ *starlark.Thread, _ string) (starlark.StringDict, error) {
	return starlark.StringDict{}, nil
}

// dedupeURLs collapses (URL, Platform) duplicates and folds URLs seen
// on every platform into a single "any" row. Per-platform output is
// sorted by platform label for determinism.
func dedupeURLs(urls []taint.CapturedURL) []taint.CapturedURL {
	type key struct{ url, platform string }
	rowByKey := map[key]taint.CapturedURL{}
	platsByURL := map[string]map[string]bool{}
	allPlats := map[string]bool{}
	urlOrder := []string{}
	urlSeen := map[string]bool{}

	for _, u := range urls {
		k := key{u.URL, u.Platform}
		if _, dup := rowByKey[k]; !dup {
			rowByKey[k] = u
		}
		if platsByURL[u.URL] == nil {
			platsByURL[u.URL] = map[string]bool{}
		}
		platsByURL[u.URL][u.Platform] = true
		allPlats[u.Platform] = true
		if !urlSeen[u.URL] {
			urlSeen[u.URL] = true
			urlOrder = append(urlOrder, u.URL)
		}
	}

	var out []taint.CapturedURL
	for _, url := range urlOrder {
		plats := platsByURL[url]
		if len(plats) == len(allPlats) && len(allPlats) > 1 {
			// Pick the lexicographically first platform key to seed the
			// "any" row — keeps the dedupe output independent of map
			// iteration order.
			platKeys := make([]string, 0, len(plats))
			for p := range plats {
				platKeys = append(platKeys, p)
			}
			sort.Strings(platKeys)
			row := rowByKey[key{url, platKeys[0]}]
			row.Platform = "any"
			out = append(out, row)
			continue
		}
		platKeys := make([]string, 0, len(plats))
		for p := range plats {
			platKeys = append(platKeys, p)
		}
		sort.Strings(platKeys)
		for _, p := range platKeys {
			out = append(out, rowByKey[key{url, p}])
		}
	}
	return out
}
