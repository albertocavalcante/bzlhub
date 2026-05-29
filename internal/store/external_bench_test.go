package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/albertocavalcante/assay/report"
)

// BenchmarkExternalSurfaceRead measures the cost of the two-query read
// path (GetExternalRefs + GetExternalForkErrors).
//
// Baseline on Apple M4 (2026-05): 10/100/1000 refs → 70 µs / 174 µs /
// 2.5 ms. Cost is dominated by per-row scan + alloc, not the query
// count. A JOIN refactor was considered and rejected — the second
// query adds ~5% overhead vs. ~95% from row materialization, which
// the JOIN doesn't change. Bench stays as a regression guard.
//
// To run:
//
//	go test ./internal/store -bench BenchmarkExternalSurfaceRead -benchmem -run '^$'
func BenchmarkExternalSurfaceRead(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("refs=%d", n), func(b *testing.B) {
			s := openTempStore(b)
			ctx := context.Background()
			if err := s.WriteReport(ctx, &report.ModuleReport{Name: "m", Version: "1"}); err != nil {
				b.Fatal(err)
			}
			refs := make([]ExternalRef, n)
			for i := range refs {
				refs[i] = ExternalRef{
					URL:      fmt.Sprintf("https://example.com/%d/file.tar.gz", i),
					Host:     "example.com",
					Class:    "github-archive",
					Platform: "any",
					File:     fmt.Sprintf("deps_%d.bzl", i),
					RuleName: fmt.Sprintf("dep_%d", i),
				}
			}
			forkErrs := []ExternalForkError{
				{Platform: "linux/amd64", Message: "fork error"},
				{Platform: "darwin/arm64", Message: "fork error"},
			}
			if err := s.WriteExternalRefs(ctx, "m", "1", refs, forkErrs); err != nil {
				b.Fatal(err)
			}

			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.GetExternalRefs(ctx, "m", "1"); err != nil {
					b.Fatal(err)
				}
				if _, err := s.GetExternalForkErrors(ctx, "m", "1"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkWriteUseExtensionUsages measures the per-row insert cost.
//
// Baseline on Apple M4 (2026-05): 10/100/1000 usages → 100 µs / 840 µs
// / 9.2 ms (linear, ~9 µs/row). The SQLite driver's internal
// prepared-statement cache already amortizes the prepare cost across
// calls within a transaction, so explicit Prepare-outside-loop was
// considered and rejected — the speedup would be < 10% and the
// readability cost is real. Bench stays as a regression guard.
//
// To run:
//
//	go test ./internal/store -bench BenchmarkWriteUseExtensionUsages -benchmem -run '^$'
func BenchmarkWriteUseExtensionUsages(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("usages=%d", n), func(b *testing.B) {
			s := openTempStore(b)
			ctx := context.Background()
			if err := s.WriteReport(ctx, &report.ModuleReport{Name: "consumer", Version: "1"}); err != nil {
				b.Fatal(err)
			}
			usages := make([]UseExtensionUsage, n)
			for i := range usages {
				usages[i] = UseExtensionUsage{
					ConsumerModule:  "consumer",
					ConsumerVersion: "1",
					ExtensionFile:   "@rules_x//:extensions.bzl",
					ExtensionName:   "ext",
					TagIndex:        i,
					TagName:         "configure",
					TagAttrsJSON:    `{"k":"v"}`,
				}
			}

			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				if err := s.WriteUseExtensionUsages(ctx, "consumer", "1", usages); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
