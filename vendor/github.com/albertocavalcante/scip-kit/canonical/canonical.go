// Package canonical puts a SCIP index into its canonical form: one wire
// encoding per range, and a total order over everything that has one.
//
// It exists because three upstream helpers cannot be used for this:
//
//   - Occurrence.Compare stops after range and symbol, and SortDiagnostics
//     after severity and message, so neither is a TOTAL order. sort.Slice is
//     not stable, so two runs over the same input can disagree.
//   - FlattenOccurrences compares the DEPRECATED range field, making it a
//     no-op on any index a v0.9.0+ producer emits.
//   - FlattenSymbols merges only Documentation and Relationships, silently
//     discarding Kind, DisplayName, SignatureDocumentation and
//     EnclosingSymbol.
//
// Every function here is deterministic: the same input always produces the
// same bytes, which is what lets a merged or generated index be diffed and
// checked into a repository.
package canonical

import (
	"sort"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// RangeEncoding selects how occurrence ranges are written to the output.
//
// Schema v0.9.0 replaced the flat `range` field with a typed oneof
// (single_line_range / multi_line_range) and deprecated the old one. That
// leaves FOUR wire representations of the same range, so two semantically
// identical indexes can differ byte for byte. Everything is decoded through
// SourceRange() and re-encoded through exactly one policy.
type RangeEncoding int

const (
	// RangeLegacy writes the deprecated flat `range` field and clears the
	// typed oneof.
	//
	// This is the default, deliberately, against upstream's "new producers
	// SHOULD set typed_range" guidance. Consumers still pinned to bindings
	// older than v0.9.0 read only the flat field: a typed-only index gives
	// them nil ranges for every occurrence. RangeLegacy remains the default
	// for broad interop; callers with current consumers can select RangeTyped.
	RangeLegacy RangeEncoding = iota

	// RangeTyped writes the typed oneof and clears the deprecated field.
	RangeTyped

	// RangeBoth writes both. Interop-maximal, at a size cost. Per the spec
	// the typed form takes precedence when both are present.
	RangeBoth
)

func (e RangeEncoding) String() string {
	switch e {
	case RangeTyped:
		return "typed"
	case RangeBoth:
		return "both"
	default:
		return "legacy"
	}
}

// ApplyRangeEncoding rewrites one occurrence's range fields to the chosen
// encoding. An occurrence with no readable range is left alone -- that is a
// producer bug for `scip lint` to report, not something to invent a range for.
func ApplyRangeEncoding(occ *scip.Occurrence, enc RangeEncoding) {
	if occ == nil {
		return
	}
	if r, ok := occ.SourceRange(); ok {
		switch enc {
		case RangeLegacy:
			occ.TypedRange = nil
			occ.Range = r.SCIPRange()
		case RangeTyped:
			occ.SetSourceRange(r)
		case RangeBoth:
			occ.TypedRange = r.AsTypedRange()
			occ.Range = r.SCIPRange()
		}
	}
	if r, ok := occ.EnclosingSourceRange(); ok {
		switch enc {
		case RangeLegacy:
			occ.TypedEnclosingRange = nil
			occ.EnclosingRange = r.SCIPRange()
		case RangeTyped:
			occ.SetEnclosingSourceRange(r)
		case RangeBoth:
			occ.TypedEnclosingRange = r.AsTypedEnclosingRange()
			occ.EnclosingRange = r.SCIPRange()
		}
	}
}

// OccurrenceLess is a TOTAL order on occurrences.
//
// Upstream's Occurrence.Compare stops after range and symbol, so two
// occurrences that share both but differ in roles or syntax kind compare
// equal -- and sort.Slice is not stable, so their relative order varies run to
// run and the output stops being byte-reproducible. This continues through
// every remaining field, ending with the marshaled bytes, which is total by
// construction.
func OccurrenceLess(a, b *scip.Occurrence) bool {
	return CompareOccurrence(a, b) < 0
}

// CompareOccurrence orders two occurrences and returns -1, 0 or 1.
//
// This is a TOTAL order, which is the whole reason it exists: upstream's
// Occurrence.Compare is not, so sorting with it can produce different output
// for the same input between runs. Ranges are read through SourceRange so both
// the flat and the typed encoding are handled.
func CompareOccurrence(a, b *scip.Occurrence) int {
	ra, aok := a.SourceRange()
	rb, bok := b.SourceRange()
	switch {
	case !aok && bok:
		return -1
	case aok && !bok:
		return 1
	case aok && bok:
		if c := ra.CompareStrict(rb); c != 0 {
			return c
		}
	}
	if c := cmpString(a.Symbol, b.Symbol); c != 0 {
		return c
	}
	if c := cmpInt32(a.SymbolRoles, b.SymbolRoles); c != 0 {
		return c
	}
	if c := cmpInt32(int32(a.SyntaxKind), int32(b.SyntaxKind)); c != 0 {
		return c
	}
	if c := cmpStrings(a.OverrideDocumentation, b.OverrideDocumentation); c != 0 {
		return c
	}
	// Last resort: deterministic marshaling. Reached only when every
	// semantic field ties, which keeps the cost off the common path while
	// making the order total rather than merely usually-total.
	return cmpBytes(marshalDeterministic(a), marshalDeterministic(b))
}

// Occurrences dedupes and orders one document's occurrences.
//
// Two occurrences with the same (range, symbol) are the same fact observed
// twice -- typically the same file indexed by two inputs -- so they collapse:
// roles are ORed, the first specified syntax kind wins, and the additive
// lists are unioned. Anything else is kept.
//
// This is NOT upstream's FlattenOccurrences, which compares the DEPRECATED
// range field directly and is therefore a no-op on a typed-range index.
func Occurrences(occs []*scip.Occurrence, enc RangeEncoding) (kept []*scip.Occurrence, deduped int) {
	type key struct {
		rng    string
		symbol string
	}
	index := map[key]*scip.Occurrence{}
	out := make([]*scip.Occurrence, 0, len(occs))

	for _, occ := range occs {
		if occ == nil {
			continue
		}
		ApplyRangeEncoding(occ, enc)

		r, ok := occ.SourceRange()
		if !ok {
			out = append(out, occ) // no range to key on; keep as-is
			continue
		}
		k := key{rng: r.String(), symbol: occ.Symbol}
		prev, seen := index[k]
		if !seen {
			index[k] = occ
			out = append(out, occ)
			continue
		}
		prev.SymbolRoles |= occ.SymbolRoles
		if prev.SyntaxKind == scip.SyntaxKind_UnspecifiedSyntaxKind {
			prev.SyntaxKind = occ.SyntaxKind
		}
		prev.OverrideDocumentation = unionStrings(prev.OverrideDocumentation, occ.OverrideDocumentation)
		prev.Diagnostics = unionDiagnostics(prev.Diagnostics, occ.Diagnostics)
		deduped++
	}

	for _, occ := range out {
		SortDiagnostics(occ.Diagnostics)
	}
	sort.SliceStable(out, func(i, j int) bool { return OccurrenceLess(out[i], out[j]) })
	return out, deduped
}

// Document orders everything inside one document.
func Document(doc *scip.Document, enc RangeEncoding) (deduped int) {
	if doc == nil {
		return 0
	}
	doc.Occurrences, deduped = Occurrences(doc.Occurrences, enc)

	sort.SliceStable(doc.Symbols, func(i, j int) bool {
		return doc.Symbols[i].GetSymbol() < doc.Symbols[j].GetSymbol()
	})
	for _, si := range doc.Symbols {
		if si == nil {
			continue
		}
		si.Relationships = MergeRelationships(si.Relationships, nil)
		if sig := si.SignatureDocumentation; sig != nil {
			// Signature occurrences index into Signature.Text, so they are
			// never rebased -- but the same wire normalization applies, and
			// the same total order keeps them reproducible.
			sig.Occurrences, _ = Occurrences(sig.Occurrences, enc)
		}
	}
	return deduped
}

// SortDiagnostics orders diagnostics totally.
//
// Upstream's SortDiagnostics keys on (Severity, Message) only, which leaves
// two diagnostics differing solely in Code, Source or Tags comparing equal.
func SortDiagnostics(ds []*scip.Diagnostic) {
	for _, d := range ds {
		if d == nil {
			continue
		}
		sort.Slice(d.Tags, func(i, j int) bool { return d.Tags[i] < d.Tags[j] })
		d.Tags = dedupeTags(d.Tags)
	}
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if c := cmpInt32(int32(a.GetSeverity()), int32(b.GetSeverity())); c != 0 {
			return c < 0
		}
		if c := cmpString(a.GetCode(), b.GetCode()); c != 0 {
			return c < 0
		}
		if c := cmpString(a.GetSource(), b.GetSource()); c != 0 {
			return c < 0
		}
		if c := cmpString(a.GetMessage(), b.GetMessage()); c != 0 {
			return c < 0
		}
		return cmpTags(a.GetTags(), b.GetTags()) < 0
	})
}

func dedupeTags(tags []scip.DiagnosticTag) []scip.DiagnosticTag {
	if len(tags) == 0 {
		return nil
	}
	out := tags[:1]
	for _, t := range tags[1:] {
		if t != out[len(out)-1] {
			out = append(out, t)
		}
	}
	return out
}

func unionStrings(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]bool, len(a))
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			a = append(a, s)
		}
	}
	return a
}

// unionDiagnostics appends b's diagnostics that a does not already carry,
// comparing by full proto equality rather than by message alone.
func unionDiagnostics(a, b []*scip.Diagnostic) []*scip.Diagnostic {
	for _, d := range b {
		if d == nil {
			continue
		}
		dup := false
		for _, existing := range a {
			if proto.Equal(existing, d) {
				dup = true
				break
			}
		}
		if !dup {
			a = append(a, d)
		}
	}
	return a
}

// marshalDeterministic is used only as a final tiebreak, so a marshaling
// failure degrades to "these compare equal" rather than panicking.
func marshalDeterministic(m proto.Message) []byte {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// cmpInt compares two ints. Lengths are compared with this rather than by
// converting to int32: a conversion truncates, and a truncated length can
// invert the comparison -- 1<<31 elements would read as negative and sort
// before an empty slice. Unreachable at any real index size, but this is the
// package whose entire purpose is supplying total orders that upstream's are
// not, so its own comparators should not have a width bug in them.
func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpInt32(a, b int32) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpStrings(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := cmpString(a[i], b[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(a), len(b))
}

func cmpTags(a, b []scip.DiagnosticTag) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := cmpInt32(int32(a[i]), int32(b[i])); c != 0 {
			return c
		}
	}
	return cmpInt(len(a), len(b))
}

func cmpBytes(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return cmpInt(len(a), len(b))
}

// MergeRelationships groups by target symbol, ORs the four booleans within
// each group, and emits sorted by target.
//
// Upstream's FlattenRelationship does the OR but finishes with `for ... range
// map`, so its output order is not deterministic. Sorting is what makes a
// merged index byte-reproducible.
func MergeRelationships(a, b []*scip.Relationship) []*scip.Relationship {
	byTarget := map[string]*scip.Relationship{}
	order := []string{}

	add := func(rels []*scip.Relationship) {
		for _, r := range rels {
			if r == nil || r.Symbol == "" {
				continue
			}
			cur, ok := byTarget[r.Symbol]
			if !ok {
				cp := proto.Clone(r).(*scip.Relationship)
				byTarget[r.Symbol] = cp
				order = append(order, r.Symbol)
				continue
			}
			cur.IsReference = cur.IsReference || r.IsReference
			cur.IsImplementation = cur.IsImplementation || r.IsImplementation
			cur.IsTypeDefinition = cur.IsTypeDefinition || r.IsTypeDefinition
			cur.IsDefinition = cur.IsDefinition || r.IsDefinition
		}
	}
	add(a)
	add(b)

	if len(order) == 0 {
		return nil
	}
	sort.Strings(order)
	out := make([]*scip.Relationship, 0, len(order))
	for _, sym := range order {
		out = append(out, byTarget[sym])
	}
	return out
}

// SortOccurrences orders occurrences into the canonical ascending-by-range
// form, in place.
//
// This exists as its own entry point because a producer wants ordering alone.
// Occurrences() additionally dedupes and rewrites the range encoding, which is
// a merger's job, not an indexer's -- and the alternative, every consumer
// writing its own sort.SliceStable around OccurrenceLess, is precisely the
// duplication this package exists to remove.
//
// The order is total (see CompareOccurrence), which matters because
// sort.SliceStable is stable only with respect to the comparator it is given:
// a comparator that reports two distinct occurrences as equal leaves their
// relative order to the input, and two runs over the same AST can then
// disagree.
func SortOccurrences(occs []*scip.Occurrence) {
	sort.SliceStable(occs, func(i, j int) bool { return OccurrenceLess(occs[i], occs[j]) })
}
