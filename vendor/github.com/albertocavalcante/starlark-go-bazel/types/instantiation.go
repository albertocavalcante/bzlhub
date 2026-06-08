package types

import "github.com/albertocavalcante/starlark-go-bazel/taint"

// RepositoryRuleFromInstantiation type-asserts a
// taint.RuleInstantiation.Rule to *RepositoryRuleClass. Returns nil
// if the assertion fails.
//
// Rationale: taint stores Rule as starlark.Value to keep the taint
// package free of a `types` import (avoiding the cycle taint ←
// stub/eval ← taint, plus types ← taint). Consumers that want the
// typed value would otherwise sprinkle `r.Rule.(*types.RepositoryRuleClass)`
// at every call site. This accessor is the single supported way.
func RepositoryRuleFromInstantiation(r taint.RuleInstantiation) *RepositoryRuleClass {
	rc, _ := r.Rule.(*RepositoryRuleClass)
	return rc
}
