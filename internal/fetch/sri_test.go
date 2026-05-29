package fetch

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"strings"
	"testing"
)

func TestVerifyingReaderRoundtrip(t *testing.T) {
	data := []byte("hello canopy")
	sum := sha256.Sum256(data)
	sri := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])

	vr := NewVerifyingReader(bytes.NewReader(data), sri)
	got, err := io.ReadAll(vr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("read mismatch: %q != %q", got, data)
	}
	if err := vr.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyingReaderMismatch(t *testing.T) {
	vr := NewVerifyingReader(bytes.NewReader([]byte("hello canopy")), "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if _, err := io.ReadAll(vr); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := vr.Verify(); err == nil {
		t.Fatalf("expected mismatch error, got nil")
	}
}

func TestVerifyingReaderEmptyIntegritySkips(t *testing.T) {
	vr := NewVerifyingReader(bytes.NewReader([]byte("anything")), "")
	if _, err := io.ReadAll(vr); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := vr.Verify(); err != nil {
		t.Fatalf("expected nil for empty integrity, got %v", err)
	}
}

func TestVerifyingReaderMalformed(t *testing.T) {
	for _, bad := range []string{"nodash", "md5-abc", "sha256-not-base64-!!"} {
		vr := NewVerifyingReader(strings.NewReader("x"), bad)
		if _, err := io.ReadAll(vr); err != nil {
			t.Fatalf("read: %v", err)
		}
		err := vr.Verify()
		if err == nil {
			t.Fatalf("want error for %q, got nil", bad)
		}
	}
}

func TestSRIFormatting(t *testing.T) {
	sum := sha256.Sum256([]byte("x"))
	got := SRI(sum[:])
	want := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("SRI mismatch: %q vs %q", got, want)
	}
}
