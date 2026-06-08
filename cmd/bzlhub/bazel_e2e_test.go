package main

// E2E: real Bazel resolves a module closure THROUGH a real canopy
// serve process. Gated by BZLHUB_BAZEL_LIVE=1 because it requires
// (a) `bazel` (or `bazelisk`) on $PATH and (b) network to
// bcr.bazel.build for the implicit-deps cascade.
//
// What's hermetic vs not:
//
//   Hermetic — under testdata/bazel-fixture/:
//     bazel_registry.json
//     modules/test_leaf/{metadata.json, 1.0.0/MODULE.bazel}
//     modules/test_mid/{metadata.json, 1.0.0/MODULE.bazel}   (→ test_leaf)
//     modules/test_root/{metadata.json, 1.0.0/MODULE.bazel}  (→ test_mid)
//
//   Not hermetic — Bazel implicitly resolves rules_license,
//   platforms, bazel_tools, etc. at module-graph time. Those are
//   served via canopy's federation cascade to bcr.bazel.build. That's
//   the headline use case the test exists to validate: a private
//   internal registry (the fixture) + the public BCR (cascade
//   upstream) appearing as one registry URL to Bazel.
//
// Determinism:
//   - Bazel runs with --noworkspace_rc --nohome_rc --nosystem_rc so
//     local rc files don't leak in.
//   - --output_user_root and --repository_cache both point at
//     t.TempDir() so a prior Bazel run on this host doesn't seed
//     cached deps.
//   - canopy listens on a 127.0.0.1:0 random free port (no clash
//     with a running canopy on :8080 from manual demos).
//   - The fixture is a static snapshot in repo; only BCR-side
//     state can drift (a yanked rules_license@1.0.0, an outage).
//     BCR has historically been stable enough for this to be a
//     low-noise gate.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	// bazelEnvGate is the explicit opt-in. CI / dev runs without it
	// skip the test cleanly; only operators who want to exercise the
	// real Bazel ↔ canopy ↔ BCR chain enable it.
	bazelEnvGate = "BZLHUB_BAZEL_LIVE"
	// bazelMinMajor is the floor for bzlmod support. Bazel 7
	// introduced bzlmod as default; earlier versions can opt in via
	// --enable_bzlmod, but we don't bother because operators on 6 or
	// older are out of the federation story anyway.
	bazelMinMajor = 7
)

// TestE2E_BazelModGraphResolvesViaCanopy proves the full chain:
//
//	bazel mod graph → canopy(--registry) → cascade
//	                                       ├─ local fixture (test_*)
//	                                       └─ bcr.bazel.build (implicit deps)
//
// Pass criteria:
//   - bazel mod graph exits 0
//   - stdout contains test_root, test_mid, test_leaf at the pinned
//     versions
//   - canopy's stderr shows it served at least one /modules/test_root/
//     path (proves the cascade actually routed through canopy)
func TestE2E_BazelModGraphResolvesViaCanopy(t *testing.T) {
	if os.Getenv(bazelEnvGate) != "1" {
		t.Skipf("%s not set; skipping E2E Bazel test", bazelEnvGate)
	}
	bazelBin, err := exec.LookPath("bazel")
	if err != nil {
		// bazelisk is fine too — same CLI surface.
		if bazelBin, err = exec.LookPath("bazelisk"); err != nil {
			t.Skipf("neither bazel nor bazelisk on $PATH: %v", err)
		}
	}
	if err := requireBazelVersion(bazelBin, bazelMinMajor); err != nil {
		t.Skipf("bazel version check failed: %v", err)
	}

	// Build canopy fresh as a test artifact so the test binary doesn't
	// depend on /tmp/canopy from prior manual demos.
	canopyBin := buildCanopy(t)

	// Free port for canopy. Listen → grab addr → close → spawn canopy
	// on that addr. Race window exists between close + canopy's bind
	// but is negligible on 127.0.0.1.
	canopyAddr := freePort(t)
	canopyURL := "http://" + canopyAddr

	// Locate fixture relative to this test file's directory.
	fixtureDir, err := filepath.Abs("testdata/bazel-fixture")
	if err != nil {
		t.Fatalf("locate fixture: %v", err)
	}
	if _, err := os.Stat(fixtureDir); err != nil {
		t.Fatalf("fixture missing at %s: %v", fixtureDir, err)
	}

	// Spawn canopy with the fixture as --root and BCR as the upstream
	// cascade target.
	canopyCtx, canopyCancel := context.WithCancel(t.Context())
	defer canopyCancel()
	canopyCmd := exec.CommandContext(canopyCtx, canopyBin, "serve",
		"--root", fixtureDir,
		"--upstream", "https://bcr.bazel.build",
		"--addr", canopyAddr,
	)
	// Disable shadow detection in the E2E to reduce log noise + BCR
	// traffic (we don't assert collisions in this test).
	canopyCmd.Env = append(os.Environ(), "BZLHUB_DISABLE_SHADOW_DETECTION=true")
	var canopyLog bytes.Buffer
	canopyCmd.Stdout = &canopyLog
	canopyCmd.Stderr = &canopyLog
	if err := canopyCmd.Start(); err != nil {
		t.Fatalf("start canopy: %v", err)
	}
	t.Cleanup(func() {
		_ = canopyCmd.Process.Kill()
		_ = canopyCmd.Wait()
	})

	// Wait for canopy to bind. 5s is generous — boot is <1s normally.
	if err := waitForHTTP(canopyURL+"/bazel_registry.json", 5*time.Second); err != nil {
		t.Fatalf("canopy didn't come up:\n--- canopy log ---\n%s\n--- err: %v", canopyLog.String(), err)
	}

	// Build the test workspace under a temp dir. MODULE.bazel
	// declares a single bazel_dep on test_root so resolution walks
	// test_root → test_mid → test_leaf plus the implicit Bazel deps.
	workspace := t.TempDir()
	writeFile(t, filepath.Join(workspace, "MODULE.bazel"),
		`module(name = "canopy_e2e_workspace", version = "0.0.0")
bazel_dep(name = "test_root", version = "1.0.0")
`)
	// Empty BUILD.bazel so Bazel doesn't complain about the package.
	writeFile(t, filepath.Join(workspace, "BUILD.bazel"), "")

	// Bazel's output bases. Both in t.TempDir() so we never pollute
	// the host's ~/.cache/bazel.
	outputUserRoot := t.TempDir()
	repoCache := t.TempDir()

	// Drive `bazel mod graph` against canopy. Hermetic Bazel
	// invocation — no rc files from anywhere.
	bazelCtx, bazelCancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer bazelCancel()
	bazelCmd := exec.CommandContext(bazelCtx, bazelBin,
		"--noworkspace_rc",
		"--nohome_rc",
		"--nosystem_rc",
		"--output_user_root="+outputUserRoot,
		"mod", "graph",
		"--registry="+canopyURL+"/",
		"--repository_cache="+repoCache,
	)
	bazelCmd.Dir = workspace
	var bazelOut bytes.Buffer
	bazelCmd.Stdout = &bazelOut
	bazelCmd.Stderr = &bazelOut
	if err := bazelCmd.Run(); err != nil {
		t.Fatalf("bazel mod graph failed: %v\n--- bazel output ---\n%s\n--- canopy log ---\n%s",
			err, bazelOut.String(), canopyLog.String())
	}

	// Assert that the workspace's declared closure is present in the
	// graph. Match on substring rather than exact format because
	// `bazel mod graph` output evolves across Bazel versions.
	got := bazelOut.String()
	for _, want := range []string{"test_root@1.0.0", "test_mid@1.0.0", "test_leaf@1.0.0"} {
		if !strings.Contains(got, want) {
			t.Errorf("bazel mod graph missing %q in output:\n%s", want, got)
		}
	}

	// Assert canopy actually served the fixture (cascade hit local).
	// Without this check, a regression that routes everything to
	// BCR (skipping the local primary entirely) would silently pass.
	if !strings.Contains(canopyLog.String(), "/modules/test_root") {
		t.Errorf("canopy log doesn't show test_root resolution — did Bazel reach canopy?\n--- canopy log ---\n%s", canopyLog.String())
	}
}

// buildCanopy compiles a fresh canopy binary in the test's temp dir.
// Avoids depending on /tmp/canopy from prior manual runs and ensures
// we test what's in the current working tree.
func buildCanopy(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "canopy")
	cmd := exec.Command("go", "build", "-o", out, ".")
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	if buildOut, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build canopy:\n%s\nerr: %v", buildOut, err)
	}
	return out
}

// freePort picks a free port by listening on :0 + immediately
// releasing. Subject to a small race before the caller binds; on
// 127.0.0.1 that's overwhelmingly safe.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// waitForHTTP polls url until it returns 200 OR the deadline passes.
func waitForHTTP(url string, deadline time.Duration) error {
	end := time.Now().Add(deadline)
	client := http.Client{Timeout: 500 * time.Millisecond}
	for time.Now().Before(end) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("waitForHTTP: deadline exceeded for " + url)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// requireBazelVersion runs `bazel --version` and refuses < min.
// Bazel's output is "bazel <semver>" or "bazel <semver> (...)".
func requireBazelVersion(bazel string, minMajor int) error {
	out, err := exec.Command(bazel, "--version").Output()
	if err != nil {
		return fmt.Errorf("`%s --version`: %w", bazel, err)
	}
	gotMajor, err := parseBazelMajorVersion(string(out))
	if err != nil {
		return err
	}
	if gotMajor < minMajor {
		return fmt.Errorf("bazel %d < min %d", gotMajor, minMajor)
	}
	return nil
}

func parseBazelMajorVersion(out string) (int, error) {
	line := strings.TrimSpace(out)
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0, fmt.Errorf("unexpected --version output: %q", line)
	}
	if parts[0] != "bazel" {
		return 0, fmt.Errorf("unexpected --version command name in %q", line)
	}
	major := strings.SplitN(parts[1], ".", 2)[0]
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0, fmt.Errorf("unexpected bazel major version in %q: %w", line, err)
	}
	return n, nil
}

func TestParseBazelMajorVersion(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want int
	}{
		{name: "single digit", out: "bazel 7.4.1", want: 7},
		{name: "two digits", out: "bazel 10.0.0", want: 10},
		{name: "extra suffix", out: "bazel 8.2.0 (release)", want: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBazelMajorVersion(tt.out)
			if err != nil {
				t.Fatalf("parseBazelMajorVersion: %v", err)
			}
			if got != tt.want {
				t.Fatalf("major = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseBazelMajorVersionRejectsUnexpectedOutput(t *testing.T) {
	for _, out := range []string{"not-a-bazel-version", "other 10.0.0"} {
		if _, err := parseBazelMajorVersion(out); err == nil {
			t.Fatalf("parseBazelMajorVersion accepted malformed output %q", out)
		}
	}
}
