package dialect

// Bazel returns the Dialect implementation for Bazel's Starlark.
func Bazel() Dialect { return bazelDialect{} }

type bazelDialect struct{}

func (bazelDialect) Name() string { return "bazel" }

func (bazelDialect) IsRuleSymbol(s string) bool     { return s == "rule" }
func (bazelDialect) IsProviderSymbol(s string) bool { return s == "provider" }
func (bazelDialect) IsAspectSymbol(s string) bool   { return s == "aspect" }
func (bazelDialect) IsRepositoryRuleSymbol(s string) bool {
	return s == "repository_rule"
}
func (bazelDialect) IsModuleExtensionSymbol(s string) bool {
	return s == "module_extension"
}
func (bazelDialect) IsTagClassSymbol(s string) bool { return s == "tag_class" }
func (bazelDialect) IsToolchainTypeSymbol(s string) bool {
	return s == "toolchain_type"
}
func (bazelDialect) IsToolchainSymbol(s string) bool { return s == "toolchain" }

// Network-fetch primitives recognized in repository_rule / module_extension bodies.
// These come from Bazel's repository_ctx + the @bazel_tools http_archive family.
//
// Not exhaustive — covers the patterns most modules use. Extend as we encounter
// real-world misses.
var bazelNetworkFetchSymbols = map[string]bool{
	"download":             true, // repository_ctx.download
	"download_and_extract": true,
	"http_archive":         true,
	"http_file":            true,
	"http_jar":             true,
	"git_repository":       true,
	"new_git_repository":   true,
}

func (bazelDialect) IsNetworkFetchAPI(s string) bool {
	return bazelNetworkFetchSymbols[s]
}

// System-exec primitives. ctx.execute is the universal one in repository_ctx;
// some modules call into shell, docker, etc. via execute(["docker", ...]).
var bazelSystemExecSymbols = map[string]bool{
	"execute": true, // repository_ctx.execute / module_ctx.execute
}

func (bazelDialect) IsSystemExecAPI(s string) bool {
	return bazelSystemExecSymbols[s]
}

// Compilation-rule names. A call to any of these in a non-test BUILD
// file means the module ships source that gets compiled at consumer
// build time — distinct from wrapping a downloaded binary. `_test`
// variants are intentionally excluded: tests aren't part of the
// ruleset's shipped surface.
var bazelCompilationRuleSymbols = map[string]bool{
	// Go
	"go_binary":  true,
	"go_library": true,
	// C / C++
	"cc_binary":  true,
	"cc_library": true,
	// Java
	"java_binary":  true,
	"java_library": true,
	// Kotlin (rules_kotlin)
	"kt_jvm_binary":  true,
	"kt_jvm_library": true,
	// Rust
	"rust_binary":  true,
	"rust_library": true,
	// Swift
	"swift_binary":  true,
	"swift_library": true,
	// Python
	"py_binary":  true,
	"py_library": true,
	// Scala
	"scala_binary":  true,
	"scala_library": true,
}

func (bazelDialect) IsCompilationRuleSymbol(s string) bool {
	return bazelCompilationRuleSymbols[s]
}

func (bazelDialect) AttrModuleSymbols() []string { return []string{"attr"} }
