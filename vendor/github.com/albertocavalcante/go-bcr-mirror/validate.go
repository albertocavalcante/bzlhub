package bcrmirror

import (
	"fmt"
	"strings"
)

// validateModuleName checks that name is a safe module identifier:
//   - non-empty
//   - no leading "." (would shadow hidden entries like ".git" if the
//     filesystem allowed; also confuses ListModules's hidden-skip)
//   - no path separators ("/", "\")
//   - no parent-dir component ("..")
//   - no NUL byte (defensive — Unix file APIs treat NUL as terminator)
//   - characters limited to BCR's de-facto module-name class:
//     lowercase letters, digits, "_", "-", "."
//
// Returns a wrapped ErrInvalidName when any check fails. The caller's
// errors.Is(_, ErrInvalidName) catches every variant.
//
// The chosen character set matches what canopy + upstream BCR actually
// use. Real-world examples — bazel_skylib, rules_python, abseil-cpp,
// google.protobuf — all pass. The set is intentionally narrow; if a
// future module surfaces with an unsupported character (e.g. a colon
// for a namespace prefix) we widen here deliberately, not by accident.
func validateModuleName(name string) error {
	return validateNameSegment("module", name, isModuleNameChar)
}

// validateVersion checks the version segment for BCR + canopy
// 4-component variants (e.g. "0.60.0.1"). Allows digits, dots, "-",
// "+", and ASCII letters for SemVer prerelease tags ("1.0.0-rc.1",
// "1.0.0+build5").
func validateVersion(version string) error {
	return validateNameSegment("version", version, isVersionChar)
}

// validatePatchName checks a patch file name. Allows the same chars
// as module names plus uppercase letters (BCR patches frequently have
// names like "0001-Add-FooBar.patch") and ".patch"/"diff" suffixes
// fit naturally.
func validatePatchName(name string) error {
	return validateNameSegment("patch", name, isPatchNameChar)
}

// validateNameSegment is the shared body: empty / leading-dot /
// separator / parent / NUL / char-class.
func validateNameSegment(kind, s string, allowed func(byte) bool) error {
	if s == "" {
		return fmt.Errorf("%w: empty %s name", ErrInvalidName, kind)
	}
	if strings.HasPrefix(s, ".") {
		return fmt.Errorf("%w: %s name starts with '.' (%q)", ErrInvalidName, kind, s)
	}
	if strings.ContainsAny(s, `/\`) {
		return fmt.Errorf("%w: %s name contains path separator (%q)", ErrInvalidName, kind, s)
	}
	if strings.Contains(s, "..") {
		return fmt.Errorf("%w: %s name contains '..' (%q)", ErrInvalidName, kind, s)
	}
	if strings.ContainsRune(s, '\x00') {
		return fmt.Errorf("%w: %s name contains NUL byte", ErrInvalidName, kind)
	}
	for i := 0; i < len(s); i++ {
		if !allowed(s[i]) {
			return fmt.Errorf("%w: %s name contains disallowed character at offset %d (%q)",
				ErrInvalidName, kind, i, s)
		}
	}
	return nil
}

// Character-class predicates. Inline, branch-cheap.

func isModuleNameChar(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= '0' && b <= '9') ||
		b == '_' || b == '-' || b == '.'
}

func isVersionChar(b byte) bool {
	return (b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		b == '.' || b == '-' || b == '+'
}

func isPatchNameChar(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '_' || b == '-' || b == '.'
}
