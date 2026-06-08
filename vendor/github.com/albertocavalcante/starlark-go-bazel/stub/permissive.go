// Package stub provides Permissive, the universal-stub Starlark value
// returned for symbols loaded from unresolvable external modules and
// for unstubbed builtins (`native`, `json`, etc.) in ModeLenient and
// ModeAnalysis evaluations.
//
// Permissive implements every interface a `.bzl` impl might invoke on
// an unknown value (Callable, HasAttrs, Mapping, HasBinary,
// Comparable) so chained access — `x.y[k]() + "/foo"` — cascades
// without aborting at name resolution or operator dispatch. The
// downside: any URL derived through a Permissive is opaque, which is
// why taint.Marker is embedded into the value's String() output.
//
// # Known limitations
//
//   - Permissive.Hash() returns an error, so a Permissive value used
//     as a dict key aborts the surrounding (per-platform) fork. The
//     caller's eval.ForkError surfaces the aborted platform; the
//     other platforms continue. Tracked under plan 01 §06 Q5 —
//     trade-off was "keep unhashable, surface via ForkError" over
//     "sentinel hash that lets dict semantics get weird."
//   - Permissive.Truth() returns True; ruleset code that branches on
//     `if not value:` will take the truthy branch even when the value
//     was loaded-but-unset. Adjust on real-corpus evidence.
//
// See docs/plans/01-bazel-builtins-emulation/04-permissive-and-taint.md
// for the full semantic contract.
package stub

import (
	"fmt"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/albertocavalcante/starlark-go-bazel/taint"
)

// Permissive is the universal-stub Starlark value. Returned for
// unresolvable load() symbols, stubbed `native`/`json` modules, and
// any other surface a ModeLenient/ModeAnalysis evaluation can't
// faithfully simulate. Shared is the package-level sentinel — all
// `Attr` / `Get` / `CallInternal` returns reuse it to avoid
// per-access allocation.
type Permissive struct{}

// Shared is the singleton Permissive returned from chained access.
// Exported so consumers can compare against it (e.g., to detect
// "this value originated from Permissive").
var Shared = &Permissive{}

var (
	_ starlark.Value      = (*Permissive)(nil)
	_ starlark.Callable   = (*Permissive)(nil)
	_ starlark.HasAttrs   = (*Permissive)(nil)
	_ starlark.Mapping    = (*Permissive)(nil)
	_ starlark.HasBinary  = (*Permissive)(nil)
	_ starlark.Comparable = (*Permissive)(nil)
	_ taint.IsPermissive  = (*Permissive)(nil)
)

// IsPermissive marks this type so taint.FlattenURLs can detect it
// across packages without a circular import.
func (p *Permissive) IsPermissive() {}

func (p *Permissive) String() string        { return taint.Marker }
func (p *Permissive) Type() string          { return "permissive" }
func (p *Permissive) Freeze()               {}
func (p *Permissive) Truth() starlark.Bool  { return starlark.True }
func (p *Permissive) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: permissive") }
func (p *Permissive) Name() string          { return "permissive" }

func (p *Permissive) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return Shared, nil
}

func (p *Permissive) Attr(name string) (starlark.Value, error) { return Shared, nil }
func (p *Permissive) AttrNames() []string                      { return nil }

func (p *Permissive) Get(k starlark.Value) (starlark.Value, bool, error) {
	return Shared, true, nil
}

// Binary handles string concat specially: rather than collapsing the
// whole expression to Permissive (losing the known prefix), it
// returns a marker-bearing starlark.String. taint.FlattenURLs detects
// the marker and taints the URL while preserving the recognizable
// portion. Non-string operands fall back to Shared.
//
// `side` is which side `p` is on. `perm + y` → side=Left;
// `y + perm` → side=Right. Result reflects positional order:
// `"a" + perm` → "a<permissive>"; `perm + "a"` → "<permissive>a".
func (p *Permissive) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	if op == syntax.PLUS {
		if ys, ok := starlark.AsString(y); ok {
			if side == starlark.Left {
				return starlark.String(taint.Marker + ys), nil
			}
			return starlark.String(ys + taint.Marker), nil
		}
	}
	return Shared, nil
}

// CompareSameType handles Permissive == Permissive comparisons.
// EQ returns false (we don't know if two opaques are equal —
// conservative). NEQ returns true. Ordered comparisons error (no
// sensible answer); callers see the error via ForkError rather than
// silently producing a wrong result.
//
// Cross-type comparisons (Permissive vs other types) don't reach
// this method — go.starlark.net's default Equal returns false/true
// for EQ/NEQ across types without erroring, so `if perm == "linux":`
// resolves to false and the else branch runs.
func (p *Permissive) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	switch op {
	case syntax.EQL:
		return false, nil
	case syntax.NEQ:
		return true, nil
	}
	return false, fmt.Errorf("permissive: ordered comparison %s not supported", op)
}
