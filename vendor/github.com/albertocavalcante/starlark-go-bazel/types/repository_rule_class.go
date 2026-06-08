package types

import (
	"fmt"

	"go.starlark.net/starlark"

	"github.com/albertocavalcante/starlark-go-bazel/taint"
)

// RepositoryRuleClass is the captured definition of a Starlark
// repository_rule(). Mirrors types.RuleClass for build-time rules.
//
// Bazel's actual semantics (from StarlarkRepositoryModule.java) reject
// CallInternal outside a module_extension impl with an EvalException.
// This implementation accepts the call and returns None — the
// capture-and-dispatch wiring lives in M5 (taint.Sinks +
// thread.Local("instSinkKey")). ModeStrict will tighten this to
// match Bazel; ModeLenient/ModeAnalysis will keep the permissive
// behavior.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/bazel/repository/starlark/StarlarkRepositoryModule.java
type RepositoryRuleClass struct {
	name           string
	implementation starlark.Callable
	attrs          map[string]starlark.Value
	local          bool
	environ        []string // Deprecated kwarg per Bazel docs.
	configure      bool
	remotable      bool // Experimental (gated by --experimental_repo_remote_exec).
	doc            string
	frozen         bool
}

var (
	_ starlark.Value    = (*RepositoryRuleClass)(nil)
	_ starlark.Callable = (*RepositoryRuleClass)(nil)
)

// NewRepositoryRuleClass constructs a repository_rule from its
// captured kwargs. Implementation may be nil during partial captures
// (the spike accepts this; production callers should pass a callable).
func NewRepositoryRuleClass(impl starlark.Callable, attrs map[string]starlark.Value, opts ...RepositoryRuleOption) *RepositoryRuleClass {
	r := &RepositoryRuleClass{implementation: impl, attrs: attrs}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RepositoryRuleOption configures optional kwargs.
type RepositoryRuleOption func(*RepositoryRuleClass)

func WithLocal(v bool) RepositoryRuleOption { return func(r *RepositoryRuleClass) { r.local = v } }
func WithConfigure(v bool) RepositoryRuleOption {
	return func(r *RepositoryRuleClass) { r.configure = v }
}
func WithRemotable(v bool) RepositoryRuleOption {
	return func(r *RepositoryRuleClass) { r.remotable = v }
}
func WithRepoEnviron(env []string) RepositoryRuleOption {
	return func(r *RepositoryRuleClass) { r.environ = env }
}
func WithRepoDoc(d string) RepositoryRuleOption { return func(r *RepositoryRuleClass) { r.doc = d } }

func (r *RepositoryRuleClass) String() string {
	if r.name != "" {
		return fmt.Sprintf("<repository_rule %s>", r.name)
	}
	return "<repository_rule>"
}

func (r *RepositoryRuleClass) Type() string         { return "repository_rule" }
func (r *RepositoryRuleClass) Freeze()              { r.frozen = true }
func (r *RepositoryRuleClass) Truth() starlark.Bool { return true }
func (r *RepositoryRuleClass) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable: repository_rule")
}
func (r *RepositoryRuleClass) Name() string                      { return r.name }
func (r *RepositoryRuleClass) SetName(name string)               { r.name = name }
func (r *RepositoryRuleClass) Implementation() starlark.Callable { return r.implementation }
func (r *RepositoryRuleClass) Attrs() map[string]starlark.Value  { return r.attrs }
func (r *RepositoryRuleClass) Local() bool                       { return r.local }
func (r *RepositoryRuleClass) Configure() bool                   { return r.configure }
func (r *RepositoryRuleClass) Remotable() bool                   { return r.remotable }
func (r *RepositoryRuleClass) Environ() []string                 { return r.environ }
func (r *RepositoryRuleClass) Doc() string                       { return r.doc }

// CallInternal records the instantiation into the thread-local sink
// established by InvokeModuleExtension. When called outside an
// extension (no sink in thread.Local), it's a silent no-op — the
// strict-mode "reject" behavior is deferred until ModeStrict wiring
// in M6.
func (r *RepositoryRuleClass) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	sinkAny := thread.Local(taint.InstSinkKey)
	sink, ok := sinkAny.(*[]taint.RuleInstantiation)
	if !ok || sink == nil {
		return starlark.None, nil
	}
	attrs := make(map[string]starlark.Value, len(kwargs))
	for _, kv := range kwargs {
		key, _ := starlark.AsString(kv[0])
		attrs[key] = kv[1]
	}
	*sink = append(*sink, taint.RuleInstantiation{Rule: r, Attrs: attrs})
	return starlark.None, nil
}
