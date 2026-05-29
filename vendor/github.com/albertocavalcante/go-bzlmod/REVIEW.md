# go-bzlmod Code Review: Reference Implementation Comparison

This document compares go-bzlmod against Bazel's reference implementation in `src/main/java/com/google/devtools/build/lib/bazel/bzlmod/`.

## Executive Summary

go-bzlmod is a solid implementation but has several gaps compared to Bazel's reference:

| Area                           | Status                                          | Priority |
| ------------------------------ | ----------------------------------------------- | -------- |
| Version comparison             | ⚠️ Differs from spec                             | High     |
| Module name validation         | ⚠️ Missing message detail                        | Medium   |
| Selection algorithm            | ⚠️ Simplified, missing max_compat_level strategy | High     |
| Discovery (nodep deps)         | ⚠️ Partial implementation                        | Medium   |
| Registry features              | ⚠️ Missing source.json types                     | Medium   |
| Reference annotations          | ❌ Sparse                                       | High     |
| bazel_compatibility validation | ❌ Missing                                      | Low      |

---

## 1. Version Comparison: Critical Differences

### Bazel Reference (`Version.java:64-218`)

```java
// Version format: RELEASE[-PRERELEASE][+BUILD]
// - RELEASE: sequence of identifiers (alphanumeric) separated by dots
// - PRERELEASE: optional, separated by hyphen
// - BUILD: optional, separated by plus (IGNORED for comparison)
//
// Empty version ("") compares HIGHER than everything (for NonRegistryOverride).
// Prerelease compares LOWER than release at same base version.
// Identifiers: digits-only compare numerically, others lexicographically.
// Digits-only identifiers compare BEFORE alphanumeric at same position.

private static final Comparator<Version> COMPARATOR =
    comparing(Version::isEmpty, falseFirst())  // Empty is highest
        .thenComparing(Version::release, lexicographical(Identifier.COMPARATOR))
        .thenComparing(Version::isPrerelease, trueFirst())  // Prerelease is lower
        .thenComparing(Version::prerelease, lexicographical(Identifier.COMPARATOR));
```

### go-bzlmod (`label/version.go:140-169`)

**Issues Found:**

1. **Empty version handling differs**: Bazel treats empty as HIGHEST (for overrides), but go-bzlmod returns 0 for empty comparisons.

2. **Identifier comparison differs**: Bazel compares digits-only BEFORE alphanumeric (`trueFirst()`), your code compares numeric < alphanumeric (line 193) which is correct but the semantics differ for mixed cases.

3. **Missing build metadata stripping**: Bazel explicitly ignores build metadata in normalization. Your code stores it but should normalize it away.

4. **Suffix handling is non-standard**: Your `.bcr.X` suffix handling isn't in Bazel's spec. BCR uses these but they're just part of prerelease in Bazel.

### Recommendation

```go
// label/version.go - Add reference and fix comparison

// Version represents a validated Bazel module version.
//
// Reference: Version.java lines 33-63
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java
//
// Format: RELEASE[-PRERELEASE][+BUILD]
// - Empty version compares HIGHER than all others (signals NonRegistryOverride)
// - Prerelease versions compare LOWER than release at same base
// - Build metadata is IGNORED for comparison (stripped during normalization)
type Version struct { ... }

func (v Version) Compare(other Version) int {
    // Reference: Version.java lines 182-186
    // Empty version is special: compares HIGHER than everything
    if v.IsEmpty() && !other.IsEmpty() {
        return 1  // Empty is highest
    }
    if !v.IsEmpty() && other.IsEmpty() {
        return -1
    }
    // ... rest of comparison
}
```

---

## 2. Module Name Validation

### Bazel Reference (`ModuleFileGlobals.java:68-77`)

```java
static void validateModuleName(String moduleName) throws EvalException {
    if (!RepositoryName.VALID_MODULE_NAME.matcher(moduleName).matches()) {
        throw Starlark.errorf(
            "invalid module name '%s': valid names must 1) only contain lowercase letters (a-z),"
                + " digits (0-9), dots (.), hyphens (-), and underscores (_); 2) begin with a"
                + " lowercase letter; 3) end with a lowercase letter or digit.",
            moduleName);
    }
}
```

### go-bzlmod (`label/module.go:38-46`)

Your validation is correct but the error message could be more helpful:

```go
// Current:
return Module{}, fmt.Errorf("invalid module name %q: must match pattern [a-z]([a-z0-9._-]*[a-z0-9])?", name)

// Recommended (matching Bazel's helpful message):
return Module{}, fmt.Errorf(
    "invalid module name %q: valid names must 1) only contain lowercase letters (a-z), "+
        "digits (0-9), dots (.), hyphens (-), and underscores (_); 2) begin with a "+
        "lowercase letter; 3) end with a lowercase letter or digit",
    name)
```

---

## 3. Selection Algorithm: Missing Complexity

### Bazel Reference (`Selection.java:44-84`)

Bazel's Selection handles complex scenarios you don't fully implement:

```
1. Basic case: Select highest version per module name (you have this ✓)

2. Compatibility levels: Modules with different compat levels are separate
   selection groups. Final graph can only have ONE version per module name.
   (you have this ✓)

3. Multiple-version overrides: Split selection groups by target allowed version.
   Dependencies upgrade to nearest higher-or-equal allowed version.
   (you have partial support)

4. max_compatibility_level: A DepSpec can be satisfied by MULTIPLE choices.
   Must enumerate all combinations (cartesian product) and find first valid.
   (❌ MISSING - this is significant!)
```

### Missing: Cartesian Product Strategy (`Selection.java:249-264`)

```java
// When max_compatibility_level is involved, each DepSpec could resolve to
// multiple valid versions. We enumerate through ALL combinations.
private static List<Function<DepSpec, Version>> enumerateStrategies(
    ImmutableMap<DepSpec, ImmutableList<Version>> possibleResolutionResults) {
    // Returns cartesian product of all possible resolutions
    return Lists.transform(
        Lists.cartesianProduct(possibleResolutionResults.values().asList()),
        (List<Version> choices) -> (DepSpec depSpec) -> choices.get(depSpecToPosition.get(depSpec)));
}
```

### go-bzlmod (`selection/selection.go:276-292`)

Your max_compatibility_level validation is present but you don't implement the full strategy enumeration:

```go
// You validate the constraint:
if resolvedModule.CompatLevel > dep.MaxCompatibilityLevel {
    return nil, nil, &SelectionError{...}
}

// But you don't implement the strategy enumeration that tries multiple
// valid versions when max_compatibility_level allows flexibility.
```

### Recommendation

Add a TODO or implement the full strategy enumeration:

```go
// selection/selection.go

// Run executes module selection (version resolution).
//
// Reference: Selection.java lines 266-353
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L266
//
// KNOWN LIMITATION: This implementation does not enumerate all possible
// resolution strategies when max_compatibility_level allows multiple valid
// versions for a DepSpec. Bazel computes the cartesian product of all
// possible resolutions and tries each until one produces a valid graph.
// See Selection.java lines 249-264 (enumerateStrategies).
func Run(graph *DepGraph, overrides map[string]Override) (*Result, error) {
```

---

## 4. Discovery: Nodep Dependencies

### Bazel Reference (`Discovery.java:62-78`)

Bazel runs **multiple rounds** of discovery for nodep edges:

```java
// Because of the possible existence of nodep edges, we do multiple rounds.
// In each round, we track unfulfilled nodep edges, and at the end, if any
// unfulfilled nodep edge can now be fulfilled, we run another round.
ImmutableSet<String> prevRoundModuleNames = ImmutableSet.of(root.module().getName());
while (true) {
    DiscoveryRound discoveryRound = new DiscoveryRound(env, root, prevRoundModuleNames);
    Result result = discoveryRound.run();
    // ... check if any unfulfilled nodep edges can now be fulfilled
    if (discoveryRound.unfulfilledNodepEdgeModuleNames.stream()
            .noneMatch(prevRoundModuleNames::contains)) {
        return result;
    }
}
```

### go-bzlmod (`resolver.go`)

You handle nodep deps in selection (`selection/selection.go:347-357`) but don't have multi-round discovery. Your resolver doesn't have explicit nodep edge tracking.

### Recommendation

Add a comment explaining the limitation or implement multi-round discovery:

```go
// resolver.go

// buildDependencyGraph constructs the dependency graph.
//
// Reference: Discovery.java lines 47-79
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Discovery.java
//
// KNOWN LIMITATION: Bazel runs multiple discovery rounds to handle nodep
// edges that become fulfillable after other modules are discovered.
// This implementation does single-pass discovery. Nodep deps are handled
// during selection phase (see selection/selection.go).
func (r *dependencyResolver) buildDependencyGraph(...) error {
```

---

## 5. Parser: Missing Validations

### Bazel Reference (`ModuleFileGlobals.java`)

Bazel validates several things your parser doesn't:

1. **bazel_compatibility format** (line 65-66):

```java
private static final Pattern VALID_BAZEL_COMPATIBILITY_VERSION =
    Pattern.compile("(>|<|-|<=|>=)(\\d+\\.){2}\\d+");
```

2. **module() must be first** (line 169-171):

```java
if (context.hadNonModuleCall()) {
    throw Starlark.errorf("if module() is called, it must be called before any other functions");
}
```

3. **module() can only be called once** (line 166-168):

```java
if (context.isModuleCalled()) {
    throw Starlark.errorf("the module() directive can only be called once");
}
```

4. **Absolute target patterns for register_toolchains** (line 199-211):

```java
if (!item.startsWith("//") && !item.startsWith("@")) {
    throw Starlark.errorf(
        "Expected absolute target patterns (must begin with '//' or '@')...");
}
```

### Recommendation

Add these validations to `parser.go` or `ast/parser.go`:

```go
// parser.go

// Validation: module() must be the first directive
// Reference: ModuleFileGlobals.java lines 169-171
var foundNonModule bool
for _, stmt := range f.Stmt {
    // ...
    switch buildutil.FuncName(call) {
    case "module":
        if foundNonModule {
            return nil, fmt.Errorf("module() must be called before any other functions")
        }
        // ...
    default:
        foundNonModule = true
    }
}
```

---

## 6. Registry: Missing Source Types

### Bazel Reference (`IndexRegistry.java:264-295`)

Bazel supports three source types in `source.json`:

```java
case "archive" -> { /* http_archive */ }
case "local_path" -> { /* local_repository */ }
case "git_repository" -> { /* git_repository */ }
```

### go-bzlmod

Your registry client only fetches `MODULE.bazel`. You don't parse `source.json` at all.

### Recommendation

If you want full compatibility, consider adding:

```go
// registry/source.go

// SourceJSON represents the source.json file in a registry.
// Reference: IndexRegistry.java lines 264-295
type SourceJSON struct {
    Type string `json:"type"` // "archive", "local_path", "git_repository"
}

// ArchiveSourceJSON for type="archive"
// Reference: IndexRegistry.java lines 269-279
type ArchiveSourceJSON struct {
    URL         string            `json:"url"`
    MirrorURLs  []string          `json:"mirror_urls"`
    Integrity   string            `json:"integrity"`
    StripPrefix string            `json:"strip_prefix"`
    Patches     map[string]string `json:"patches"`
    Overlay     map[string]string `json:"overlay"`
    PatchStrip  int               `json:"patch_strip"`
    ArchiveType string            `json:"archive_type"`
}
```

---

## 7. Missing Reference Annotations

### Files Needing Reference Links

| File                     | Missing References                             |
| ------------------------ | ---------------------------------------------- |
| `label/version.go`       | Version.java comparison algorithm              |
| `label/module.go`        | ModuleFileGlobals.validateModuleName           |
| `parser.go`              | ModuleFileGlobals validation rules             |
| `resolver.go`            | Discovery.java multi-round algorithm           |
| `selection/selection.go` | Already good, but add enumerateStrategies note |
| `types.go`               | InterimModule.DepSpec fields                   |

### Example Annotation Pattern

```go
// Package selection implements Bazel's module version selection algorithm.
//
// Reference Implementation:
//   - Selection.java: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java
//   - InterimModule.java: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/InterimModule.java
//   - Version.java: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java
//
// Key Differences from Bazel:
//   - Does not implement full strategy enumeration for max_compatibility_level
//   - Single-pass discovery (Bazel uses multi-round for nodep edges)
//   - Pure MVS without Bazel's automatic version upgrades
package selection
```

---

## 8. Idiomatic Go Improvements

### 8.1. Error Wrapping

Current:

```go
return nil, fmt.Errorf("parse %s: %w", filename, err)
```

Better (with sentinel errors):

```go
var ErrParseModule = errors.New("parse module file")

return nil, fmt.Errorf("%w %s: %v", ErrParseModule, filename, err)
```

### 8.2. Validation Types

Consider using a `Validator` interface for consistent validation:

```go
// validation.go

type Validator interface {
    Validate() error
}

func (m ModuleInfo) Validate() error {
    if m.Name == "" {
        return &ValidationError{Field: "name", Message: "required"}
    }
    if _, err := label.NewModule(m.Name); err != nil {
        return &ValidationError{Field: "name", Cause: err}
    }
    // ...
}
```

### 8.3. Context Usage

Your resolver.go handles context well. One improvement:

```go
// Current:
ctx, cancel := context.WithCancel(ctx)
defer cancel()

// Better (preserve parent timeout if set):
// Don't wrap if you don't need to propagate cancellation
```

### 8.4. Structured Errors

Consider a unified error hierarchy:

```go
// errors.go

type BzlmodError struct {
    Code    string // Machine-readable code matching Bazel's FailureDetails
    Message string
    Cause   error
    // Context for debugging
    Module  string
    Version string
    Path    []string
}

// Matches Bazel's ExternalDeps.Code enum
const (
    ErrCodeVersionResolution = "VERSION_RESOLUTION_ERROR"
    ErrCodeModuleNotFound    = "MODULE_NOT_FOUND"
    ErrCodeBadModule         = "BAD_MODULE"
)
```

---

## 9. Testing Against Reference

### Recommended Approach

1. **Pin a Bazel commit** for reference comparison
2. **Add test cases** that verify behavior matches Bazel
3. **Document known differences** in test comments

```go
// selection_test.go

func TestSelection_MatchesBazel(t *testing.T) {
    // Reference: Selection.java test cases
    // Bazel commit: <specific-sha>
    tests := []struct {
        name     string
        graph    DepGraph
        want     map[string]string // module -> selected version
        wantErr  string            // Expected error code
    }{
        {
            name: "basic_mvs_selects_highest",
            // A -> B@1.0, C -> B@2.0 => B@2.0 selected
        },
        {
            name: "compat_level_conflict",
            // A -> B@1.0 (compat=1), C -> B@2.0 (compat=2) => error
        },
    }
}
```

---

## 10. Action Items (Priority Order)

### High Priority

1. **Fix Version comparison** for empty version handling
2. **Add reference annotations** to all major functions
3. **Document max_compatibility_level limitation** in selection

### Medium Priority

4. **Improve error messages** to match Bazel's helpful format
5. **Add bazel_compatibility validation** in parser
6. **Document nodep discovery limitation**

### Low Priority

7. **Consider source.json parsing** for full registry support
8. **Add Validator interface** for consistent validation
9. **Structured error codes** matching Bazel's FailureDetails

---

## Reference Links

- [Selection.java](https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java)
- [Version.java](https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java)
- [ModuleFileGlobals.java](https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileGlobals.java)
- [Discovery.java](https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Discovery.java)
- [IndexRegistry.java](https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/IndexRegistry.java)
- [InterimModule.java](https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/InterimModule.java)
