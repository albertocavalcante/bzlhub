package egress_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestLint_NoRawHTTPClientOutsideEgress is canopy's anti-egress-leak
// lint check. The rule (Plan 20 PR 2 / Plan 21 / Plan 28 commit C4):
// canopy production code MUST NOT construct *http.Client* directly.
// All outbound HTTP goes through internal/egress.NewHTTPClient or
// egress.Client(ctx). The egress package is the single point of
// enforcement for the --no-egress policy + audit-log emission.
//
// Why a go-test instead of golangci-lint? Two reasons.
// 1. Zero new tooling dep. Canopy CI runs `go vet`, `go build`,
//    `go test -race`. This test runs alongside; no new step.
// 2. Self-documenting. A future contributor reading the diff sees
//    the rule + its rationale in one file, without needing to
//    learn the forbidigo plugin's matcher syntax.
//
// The test scans every .go file under the canopy repo (except
// vendor/, tests, and the egress package itself) and fails if it
// finds a composite literal of type http.Client.
//
// Exemptions are listed explicitly below. Each entry corresponds
// to a refactor on the Plan 28 commit chain; the entry is removed
// in the same commit that removes the raw construction. Final
// state: empty exemption list.
//
// To find what the failing entries look like, run:
//
//	go test -run TestLint_NoRawHTTPClientOutsideEgress ./internal/egress
func TestLint_NoRawHTTPClientOutsideEgress(t *testing.T) {
	// Exemptions: paths (relative to the canopy repo root) that
	// are KNOWN to still construct raw http.Client. Plan 28 chain
	// retires these one per commit (C5 fetch, C6 backend cascade,
	// C7 githubmeta, C8 forge + bcrprov).
	//
	// REMOVAL DISCIPLINE: when a commit refactors a file off raw
	// http.Client, the same commit removes the file's entry here.
	// The lint goes red between commits; that's the point.
	// All known violators have been refactored. Final state of the
	// Plan 28 commit chain: empty exemption list. Future violations
	// will fail the test loudly.
	exemptions := []string{}

	// Walk up from the test's directory to the repo root. Test
	// runs from internal/egress/; the repo root is two levels up.
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	var violations []string
	err = filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			// Skip non-source trees.
			if name == "vendor" || name == "node_modules" || name == ".git" ||
				name == "build" || name == "ui" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Test files may construct their own clients freely —
		// stubbed transports, httptest servers, etc.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		// The egress package itself is the ONE place raw
		// construction is permitted: NewHTTPClient is the rule's
		// implementation, not a violation of it.
		if strings.HasPrefix(rel, "internal/egress/") {
			return nil
		}

		hits, err := findHTTPClientLiterals(path)
		if err != nil {
			return err
		}
		for _, h := range hits {
			loc := rel + ":" + h
			if slices.Contains(exemptions, rel) {
				// Exempted file; logged but not failed.
				t.Logf("exempted violation: %s", loc)
				continue
			}
			violations = append(violations, loc)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("found %d raw http.Client construction(s) outside internal/egress:", len(violations))
		for _, v := range violations {
			t.Errorf("  %s", v)
		}
		t.Errorf("route through internal/egress.NewHTTPClient or egress.Client(ctx); see internal/egress/doc.go")
	}
}

// findHTTPClientLiterals returns the line:col of every
// `http.Client{...}` composite literal in the file.
func findHTTPClientLiterals(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if pkg.Name == "http" && sel.Sel.Name == "Client" {
			pos := fset.Position(lit.Pos())
			hits = append(hits, lineCol(pos))
		}
		return true
	})
	return hits, nil
}

// lineCol renders a file position as "L:C" (no filename). The
// caller prepends its own relative path so the lint output reads
// cleanly without the absolute path bleeding through.
func lineCol(p token.Position) string {
	return strings.TrimSpace((&token.Position{Line: p.Line, Column: p.Column}).String())
}
