// Package symbol handles SCIP symbol strings.
//
// A SCIP symbol is one of two shapes:
//
//	<scheme> ' ' <manager> ' ' <package-name> ' ' <version> ' ' <descriptor>+
//	'local ' <local-id>
//
// The string is opaque to the protocol -- nothing validates it at the wire
// level -- so agreement between an indexer and its consumers is convention
// rather than enforcement. That is exactly why these helpers exist in one
// place: a rule encoded twice is a rule that drifts, and this family has
// already shipped two indexers emitting symbols the reference parser rejects.
package symbol

import (
	"fmt"
	"strings"

	scip "github.com/scip-code/scip/bindings/go/scip"
)

// LocalPrefix marks a document-scoped symbol.
//
// The trailing space is part of it. A global symbol whose scheme is "localx"
// does not match, and the grammar forbids a scheme of literally "local", so a
// prefix test is exact rather than a heuristic.
const LocalPrefix = "local "

// IsLocal reports whether sym is document-scoped.
//
// Document-scoped means exactly that: the same `local 0` in two documents are
// DIFFERENT entities that legally share a string. Any consumer keying symbols
// in one flat map without a document qualifier will conflate them.
func IsLocal(sym string) bool {
	return strings.HasPrefix(sym, LocalPrefix)
}

// Local builds a document-scoped symbol from an already-safe identifier.
// Use EscapeLocalID first if the input is free-form.
func Local(id string) string {
	return LocalPrefix + id
}

// isIdentifierChar reports whether r may appear in a SCIP
// <simple-identifier>: '_', '+', '-', '$', or an ASCII letter or digit.
//
// This is the single definition of the character class. IsSimpleIdentifier
// and EscapeLocalID are the same rule read in opposite directions -- one
// tests membership, the other maps non-members out -- and they were
// previously separate implementations in separate repositories, which is how
// two views of one rule drift apart.
func isIdentifierChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z',
		r >= 'A' && r <= 'Z',
		r >= '0' && r <= '9',
		r == '_', r == '+', r == '-', r == '$':
		return true
	}
	return false
}

// IsSimpleIdentifier reports whether s is a SCIP <simple-identifier>.
//
// Note that '$' is legal here. That is why a local id may itself contain '$',
// and why any scheme that uses '$' as a separator must exclude it from the
// part it splits on rather than assuming the rest is '$'-free.
func IsSimpleIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isIdentifierChar(r) {
			return false
		}
	}
	return true
}

// EscapeLocalID maps a free-form string to a valid <simple-identifier> by
// replacing every character outside the class with '$'.
//
// The mapping is deliberately NOT injective: "a/b" and "a.b" both become
// "a$b". Callers that need uniqueness must supply it themselves, typically
// with a counter, rather than relying on the escaped form to preserve it.
func EscapeLocalID(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isIdentifierChar(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('$')
	}
	return b.String()
}

// Validate reports whether sym parses as a SCIP symbol.
//
// Validity is decided by the reference parser plus the method-disambiguator
// restriction in scip.proto, which the reference parser currently omits.
// An earlier version of this check elsewhere merely required the symbol to
// contain a space, which accepted `starlark rules.bzl#paths` -- precisely the
// shape it existed to reject. A check written from a mental model of a
// grammar encodes the mental model, not the grammar.
func Validate(sym string) error {
	if sym == "" {
		return fmt.Errorf("scip-kit: empty symbol")
	}
	parsed, err := scip.ParseSymbol(sym)
	if err != nil {
		return fmt.Errorf("scip-kit: %q is not a valid SCIP symbol: %w", sym, err)
	}
	hasMethod := false
	for _, descriptor := range parsed.Descriptors {
		if descriptor.Suffix == scip.Descriptor_Method {
			hasMethod = true
			if descriptor.Disambiguator != "" && !IsSimpleIdentifier(descriptor.Disambiguator) {
				return fmt.Errorf("scip-kit: %q has a method disambiguator that is not a simple identifier", sym)
			}
		}
	}
	// The parser also accepts unnecessary backtick quoting of a simple
	// disambiguator. Formatting the parsed method back to its canonical form
	// catches that syntax while preserving valid escaped method names.
	if hasMethod && scip.VerboseSymbolFormatter.FormatSymbol(parsed) != sym {
		return fmt.Errorf("scip-kit: %q is not a canonical SCIP method symbol", sym)
	}
	return nil
}

// IsValid is Validate as a predicate.
func IsValid(sym string) bool { return Validate(sym) == nil }

// Scheme returns the leading scheme of a symbol, or "" if it has none.
//
// For a local symbol this is "local". For anything the parser rejects it is
// still whatever precedes the first space, because the scheme is usually the
// only part a malformed symbol got right -- and knowing which producer emitted
// it is what makes the diagnostic actionable.
func Scheme(sym string) string {
	scheme, _, _ := strings.Cut(sym, " ")
	return scheme
}

// Empty is the grammar's placeholder for an absent package field.
//
// SCIP requires the manager, package name and version slots to be PRESENT even
// when unknown, and "." is how the grammar spells "nothing here". This is what
// makes a conformant symbol possible without inventing package metadata a
// Starlark file does not have.
const Empty = "."

// Package identifies the unit a symbol belongs to. A zero Package is valid and
// encodes as "." in every field.
type Package struct {
	// Manager is the packaging system, e.g. "bzlmod". Not the scheme: the
	// scheme says which tool produced the symbol, the manager says how the
	// package was resolved.
	Manager string

	// Name is the package or module name, e.g. "rules_python".
	Name string

	// Version is the resolved version, e.g. "0.40.0".
	Version string
}

func (p Package) field(s string) string {
	if s == "" {
		return Empty
	}
	return s
}

// Global builds a conformant global symbol:
//
//	<scheme> <manager> <name> <version> <descriptor>+
//
// path is a slash-separated source path and becomes one namespace descriptor
// per segment; name becomes a trailing type descriptor. So
// Global("starlark", Package{}, "pkg/rules.bzl", "foo") yields
//
//	starlark . . . pkg/rules.bzl/foo#
//
// It returns an error rather than a best-effort string, because a symbol that
// does not parse is worse than no symbol: every consumer that string-matches
// will happily store it and every consumer that parses the grammar will fail to
// resolve it, with nothing in between to notice. The whole family emitted such
// symbols for months.
func Global(scheme string, pkg Package, path, name string) (string, error) {
	if scheme == "" {
		return "", fmt.Errorf("scip-kit: symbol scheme must not be empty")
	}
	if name == "" {
		return "", fmt.Errorf("scip-kit: symbol name must not be empty")
	}
	var b strings.Builder
	b.WriteString(scheme)
	b.WriteByte(' ')
	b.WriteString(pkg.field(pkg.Manager))
	b.WriteByte(' ')
	b.WriteString(pkg.field(pkg.Name))
	b.WriteByte(' ')
	b.WriteString(pkg.field(pkg.Version))
	b.WriteByte(' ')
	if path != "" {
		b.WriteString(path)
		b.WriteByte('/')
	}
	b.WriteString(name)
	b.WriteByte('#')

	sym := b.String()
	if err := Validate(sym); err != nil {
		return "", err
	}
	return sym, nil
}

// MustGlobal is Global for call sites that cannot fail meaningfully. It panics
// on an invalid symbol, which is the correct outcome for a producer: emitting a
// index full of unparseable symbols is the failure this package exists to stop.
func MustGlobal(scheme string, pkg Package, path, name string) string {
	sym, err := Global(scheme, pkg, path, name)
	if err != nil {
		panic(err)
	}
	return sym
}
