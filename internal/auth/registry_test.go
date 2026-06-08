package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// sha256hex is a test helper — operators run something equivalent
// (`printf '%s' "$TOKEN" | shasum -a 256`) when seeding identity.json.
func sha256hex(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func writeIdentityFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "identity.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadIdentityFile_HappyPath(t *testing.T) {
	hashAlice := sha256hex("alice-token-32-bytes-XXXXXXXXXXXXXX")
	hashBob := sha256hex("bob-token-32-bytes-XXXXXXXXXXXXXXXX")
	body := `{
  "version": 1,
  "tokens": [
    {
      "token_sha256": "` + hashAlice + `",
      "identity": {"user": "alice@example.com", "email": "alice@example.com", "groups": ["eval-submitter"]}
    },
    {
      "token_sha256": "` + hashBob + `",
      "identity": {"user": "bob@example.com", "email": "bob@example.com", "groups": ["eval-approver", "eval-submitter"]}
    }
  ]
}`
	reg, err := LoadIdentityFile(writeIdentityFile(t, body))
	if err != nil {
		t.Fatalf("LoadIdentityFile: %v", err)
	}
	if reg.Size() != 2 {
		t.Errorf("Size = %d, want 2", reg.Size())
	}

	// Lookup happy path
	alice, ok := reg.Lookup("alice-token-32-bytes-XXXXXXXXXXXXXX")
	if !ok {
		t.Fatal("alice lookup miss")
	}
	if alice.Email != "alice@example.com" {
		t.Errorf("alice email = %q", alice.Email)
	}
	if alice.Source != SourceBearer {
		t.Errorf("alice source = %q, want %q", alice.Source, SourceBearer)
	}
	if len(alice.Groups) != 1 || alice.Groups[0] != "eval-submitter" {
		t.Errorf("alice groups = %v", alice.Groups)
	}

	bob, ok := reg.Lookup("bob-token-32-bytes-XXXXXXXXXXXXXXXX")
	if !ok {
		t.Fatal("bob lookup miss")
	}
	if len(bob.Groups) != 2 {
		t.Errorf("bob groups = %v, want 2 entries", bob.Groups)
	}

	// Lookup miss
	if _, ok := reg.Lookup("unknown-token"); ok {
		t.Error("unknown token should miss")
	}
	if _, ok := reg.Lookup(""); ok {
		t.Error("empty token should miss")
	}
}

func TestLoadIdentityFile_Missing(t *testing.T) {
	_, err := LoadIdentityFile(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if !errors.Is(err, ErrIdentityFileMissing) {
		t.Errorf("err = %v, want ErrIdentityFileMissing", err)
	}
}

func TestLoadIdentityFile_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"not json", "this is not json", "parse"},
		{"wrong version", `{"version": 99, "tokens": []}`, "unsupported version"},
		{"hash too short", `{"version": 1, "tokens": [{"token_sha256": "abc", "identity": {"user": "x"}}]}`, "64-char hex"},
		{"hash not hex", `{"version": 1, "tokens": [{"token_sha256": "` + strings.Repeat("z", 64) + `", "identity": {"user": "x"}}]}`, "valid hex"},
		{"no identity", `{"version": 1, "tokens": [{"token_sha256": "` + sha256hex("x") + `", "identity": {}}]}`, "user or email"},
		{"duplicate token", `{"version": 1, "tokens": [
			{"token_sha256": "` + sha256hex("x") + `", "identity": {"user": "a"}},
			{"token_sha256": "` + sha256hex("x") + `", "identity": {"user": "b"}}
		]}`, "duplicate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadIdentityFile(writeIdentityFile(t, c.body))
			if err == nil {
				t.Fatalf("want error, got nil")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}

func TestLoadIdentityFile_RejectsOversizeFile(t *testing.T) {
	// Defensive cap — a mis-mounted bind (someone points
	// BZLHUB_IDENTITY_FILE at /var/log/something) shouldn't OOM
	// canopy at boot. The legitimate identity file is ~150B per
	// token; 10 MB holds ~70k tokens which is far past any real
	// canopy deployment. Anything bigger is a configuration mistake.
	path := filepath.Join(t.TempDir(), "huge.json")
	// 11 MB of junk — just over the 10 MB cap.
	junk := strings.Repeat("a", 11*1024*1024)
	if err := os.WriteFile(path, []byte(junk), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadIdentityFile(path)
	if err == nil {
		t.Fatal("want error for oversize file, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error %q should mention 'too large'", err.Error())
	}
}

func TestLoadIdentityFile_EmptyTokensListOK(t *testing.T) {
	// "Bearer auth wired but no tokens yet" — registry should load
	// cleanly with Size() == 0; every Lookup misses.
	reg, err := LoadIdentityFile(writeIdentityFile(t, `{"version": 1, "tokens": []}`))
	if err != nil {
		t.Fatalf("LoadIdentityFile: %v", err)
	}
	if reg.Size() != 0 {
		t.Errorf("Size = %d, want 0", reg.Size())
	}
	if _, ok := reg.Lookup("any-token"); ok {
		t.Error("lookup against empty registry should miss")
	}
}

func TestRegistry_Replace_AtomicSwap(t *testing.T) {
	body1 := `{"version": 1, "tokens": [{"token_sha256": "` + sha256hex("v1-token") + `", "identity": {"user": "v1"}}]}`
	body2 := `{"version": 1, "tokens": [{"token_sha256": "` + sha256hex("v2-token") + `", "identity": {"user": "v2"}}]}`

	reg, err := LoadIdentityFile(writeIdentityFile(t, body1))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup("v1-token"); !ok {
		t.Fatal("v1 lookup miss before reload")
	}

	newReg, err := LoadIdentityFile(writeIdentityFile(t, body2))
	if err != nil {
		t.Fatal(err)
	}
	reg.Replace(newReg)

	// v1 token gone
	if _, ok := reg.Lookup("v1-token"); ok {
		t.Error("v1 token should be gone after Replace")
	}
	// v2 token present
	if _, ok := reg.Lookup("v2-token"); !ok {
		t.Error("v2 token should be present after Replace")
	}
}

func TestRegistry_NilSafe(t *testing.T) {
	var nilReg *IdentityRegistry
	if _, ok := nilReg.Lookup("anything"); ok {
		t.Error("nil registry lookup must miss")
	}
	if nilReg.Size() != 0 {
		t.Error("nil registry size must be 0")
	}
	nilReg.Replace(nil) // must not panic
}

func TestRegistry_ConcurrentLookup_NoRace(t *testing.T) {
	// Goroutines hammer Lookup while another swaps the registry —
	// shouldn't race or panic. `go test -race` enforces.
	body := `{"version": 1, "tokens": [{"token_sha256": "` + sha256hex("hot-token") + `", "identity": {"user": "x"}}]}`
	reg, err := LoadIdentityFile(writeIdentityFile(t, body))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = reg.Lookup("hot-token")
					_, _ = reg.Lookup("miss-token")
				}
			}
		})
	}

	// Swap several times.
	for range 10 {
		other, err := LoadIdentityFile(writeIdentityFile(t, body))
		if err != nil {
			t.Fatal(err)
		}
		reg.Replace(other)
	}
	close(stop)
	wg.Wait()
}
