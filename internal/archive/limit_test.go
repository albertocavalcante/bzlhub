package archive

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractTarGz_RejectsOverBudget covers the decompression-bomb
// defense: a tarball whose cumulative output exceeds maxBytes must
// abort with ErrExtractTooLarge, not extract a partial-but-large file
// and quietly succeed.
func TestExtractTarGz_RejectsOverBudget(t *testing.T) {
	a := makeTarGz(t, map[string]string{
		"big.txt": strings.Repeat("x", 2048),
	})
	dest := t.TempDir()
	_, err := ExtractTarGz(bytes.NewReader(a), dest, "", 1024)
	if err == nil {
		t.Fatal("expected ErrExtractTooLarge, got nil")
	}
	if !errors.Is(err, ErrExtractTooLarge) {
		t.Errorf("want ErrExtractTooLarge, got %v", err)
	}
}

// TestExtractTarGz_AcceptsExactBudget — a tarball that exactly fits
// in maxBytes must extract successfully. Guards against off-by-one
// in the budget check.
func TestExtractTarGz_AcceptsExactBudget(t *testing.T) {
	a := makeTarGz(t, map[string]string{
		"ok.txt": strings.Repeat("y", 1024),
	})
	dest := t.TempDir()
	n, err := ExtractTarGz(bytes.NewReader(a), dest, "", 1024)
	if err != nil {
		t.Fatalf("exact budget should succeed: %v", err)
	}
	if n != 1024 {
		t.Errorf("wrote %d, want 1024", n)
	}
}

// TestExtractTarGz_ZeroBudgetDisablesCap — the explicit "no cap"
// signal callers use in test fixtures. Production callers always
// pass MaxExtractBytes; 0 is the test escape hatch.
func TestExtractTarGz_ZeroBudgetDisablesCap(t *testing.T) {
	a := makeTarGz(t, map[string]string{
		"big.txt": strings.Repeat("x", 4096),
	})
	dest := t.TempDir()
	if _, err := ExtractTarGz(bytes.NewReader(a), dest, "", 0); err != nil {
		t.Errorf("zero budget should disable cap; got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "big.txt")); err != nil {
		t.Errorf("file should have been written: %v", err)
	}
}

// TestExtractTarGz_BudgetCappedMidEntry — a single entry larger
// than the remaining budget can't write past the cap. Without the
// per-entry LimitReader a malicious header declaring 1TB on the
// first file would write up to disk-full before bailing.
func TestExtractTarGz_BudgetCappedMidEntry(t *testing.T) {
	a := makeTarGz(t, map[string]string{
		"single.txt": strings.Repeat("z", 10_000),
	})
	dest := t.TempDir()
	n, err := ExtractTarGz(bytes.NewReader(a), dest, "", 1000)
	if err == nil {
		t.Fatal("expected ErrExtractTooLarge on oversized single entry")
	}
	if !errors.Is(err, ErrExtractTooLarge) {
		t.Errorf("want ErrExtractTooLarge, got %v", err)
	}
	// The LimitReader is bounded to remaining+1 = 1001, so we may
	// have written up to 1001 bytes before the post-write check
	// rejects. That's an order of magnitude less than the declared
	// 10000 — the cap is doing its job.
	if n > 1001 {
		t.Errorf("budget breach: wrote %d, max should be <= 1001", n)
	}
}

// TestExtractZip_RejectsOverBudget mirrors the tar test for zip.
func TestExtractZip_RejectsOverBudget(t *testing.T) {
	a := makeZip(t, map[string]string{
		"big.txt": strings.Repeat("x", 2048),
	})
	dest := t.TempDir()
	_, err := ExtractZip(bytes.NewReader(a), dest, "", 1024)
	if err == nil {
		t.Fatal("expected ErrExtractTooLarge, got nil")
	}
	if !errors.Is(err, ErrExtractTooLarge) {
		t.Errorf("want ErrExtractTooLarge, got %v", err)
	}
}

// TestExtractZip_CompressedSizeCap — even if uncompressed extraction
// would fit, an oversized compressed blob must be rejected at read
// time so a 10TB zip can't OOM the buffer before the entry walk.
func TestExtractZip_CompressedSizeCap(t *testing.T) {
	// 100KB of zeros compresses to a few KB. Set maxBytes below the
	// COMPRESSED size of the zip itself so the input-read cap fires
	// before extraction starts.
	a := makeZip(t, map[string]string{
		"big.bin": strings.Repeat("\x00", 100_000),
	})
	// makeZip's overhead means the compressed bytes will be ~200-300.
	// Set maxBytes < that to force the input cap.
	dest := t.TempDir()
	_, err := ExtractZip(bytes.NewReader(a), dest, "", 100)
	if err == nil {
		t.Fatal("expected ErrExtractTooLarge on oversized zip read")
	}
	if !errors.Is(err, ErrExtractTooLarge) {
		t.Errorf("want ErrExtractTooLarge, got %v", err)
	}
}
