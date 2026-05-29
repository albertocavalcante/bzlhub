package scipbazel

import (
	"strings"

	scip "github.com/scip-code/scip/bindings/go/scip"
)

// Summary aggregates Bazel-annotation counts across an Index. Useful
// for CLI summary lines and basic sanity checks. Counts are derived
// from the Documentation lines applied by Index (so an index that has
// not been run through scip-bazel's annotator will report zero for
// each Bazel category).
type Summary struct {
	Files            int // total Documents
	Documents        int // alias of Files; kept for forward compat
	Symbols          int // total SymbolInformation across all docs
	Rules            int
	Providers        int
	Aspects          int
	RepositoryRules  int
	ModuleExtensions int
	Macros           int
}

// Summarize walks idx and returns aggregated counts. nil idx is
// treated as an empty index.
func Summarize(idx *scip.Index) Summary {
	var s Summary
	if idx == nil {
		return s
	}
	s.Files = len(idx.Documents)
	s.Documents = s.Files
	for _, doc := range idx.Documents {
		s.Symbols += len(doc.Symbols)
		for _, sym := range doc.Symbols {
			for _, d := range sym.Documentation {
				switch {
				case strings.Contains(d, "Bazel rule"):
					s.Rules++
				case strings.Contains(d, "Bazel provider"):
					s.Providers++
				case strings.Contains(d, "Bazel aspect"):
					s.Aspects++
				case strings.Contains(d, "Bazel repository_rule"):
					s.RepositoryRules++
				case strings.Contains(d, "Bazel module_extension"):
					s.ModuleExtensions++
				case strings.Contains(d, "Bazel macro"):
					s.Macros++
				}
			}
		}
	}
	return s
}
