package main_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSignalShutdown_DaemonExitsCleanlyOnSIGTERM covers both the
// graceful-shutdown contract AND the orphan-lock takeover-log
// contract in one subprocess setup (bootstrapping a real BCR-
// shape clone for the daemon to operate against is ~25s; doing
// it once for two locked contracts is the leverage move).
//
// Three guarantees pinned:
//
//  1. Daemon takes over an orphan lock left by a "crashed" prior
//     run. Without PID-aware acquire (lock.go), the daemon would
//     ErrLocked every iteration.
//  2. The takeover emits a structured WARN log via slog so an
//     operator investigating "what happened to canopy.lock"
//     gets a forensic trail.
//  3. SIGTERM (what `systemctl stop` sends) cancels cmd.Context()
//     via signal.NotifyContext in main, propagates to
//     SyncRunLoop's ctx.Done branch, and the process exits 0
//     with the lock file removed (defer release ran).
//
// Without #3, the process would either ignore SIGTERM or exit
// non-zero, and the new lock file we wrote during takeover would
// itself be orphaned on the way out.
func TestSignalShutdown_DaemonExitsCleanlyOnSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess + signal — slow-ish for unit suite")
	}
	if os.Getenv("CI") == "" && os.Getenv("BZLHUB_RUN_SIGNAL_TEST") == "" {
		// Subprocess tests are mildly flaky on developer laptops
		// under heavy load; opt-in via CI or env to keep
		// unattended `go test ./...` from getting derailed.
		t.Skip("set BZLHUB_RUN_SIGNAL_TEST=1 or run in CI to exercise")
	}
	bin := buildCanopy(t)
	mirror, db := setupSmokeFixture(t)

	// Plant an orphan lock from a "crashed prior daemon" — we
	// spawn + kill a subprocess to harvest a verifiably-dead PID,
	// then write it as the lock content. PID-aware acquire on the
	// next iteration will detect the dead holder and take over.
	deadPID := harvestDeadPID(t)
	lockPath := filepath.Join(mirror, ".git", "canopy.lock")
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(deadPID)), 0o644); err != nil {
		t.Fatalf("plant orphan lock: %v", err)
	}

	// Capture stderr (also pass through to the test runner so
	// failures show what the daemon actually printed).
	var stderr bytes.Buffer
	cmd := exec.Command(bin, "sync", "run",
		"--mirror="+mirror, "--db="+db, "--interval=200ms")
	cmd.Stdout = os.Stdout
	cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Let the daemon do a few iterations — first one is the
	// takeover; subsequent ones validate the lock holds. 1.5s
	// at 200ms intervals = ≥6 iterations, generous slack for
	// CI variance.
	time.Sleep(1500 * time.Millisecond)

	// SIGTERM — should be caught by signal.NotifyContext and
	// propagate through cmd.Context() to SyncRunLoop, which
	// returns nil after the ctx.Done branch.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	// Bounded wait — the daemon should exit within a couple
	// seconds.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				t.Errorf("daemon exited with non-zero status %d on SIGTERM; want 0", exitErr.ExitCode())
			} else {
				t.Errorf("daemon Wait err = %v; want nil exit", err)
			}
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("daemon didn't exit within 10s of SIGTERM — signal.NotifyContext not wired?")
	}

	// Stderr must show the takeover line — proves the lock
	// recovery happened AND that the library's slog.Warn reached
	// the operator's stream.
	out := stderr.String()
	if !strings.Contains(out, "took over orphan lock") {
		t.Errorf("stderr missing orphan-lock takeover WARN; got: %s", out)
	}
	if !strings.Contains(out, strconv.Itoa(deadPID)) {
		t.Errorf("takeover log missing dead PID %d; got: %s", deadPID, out)
	}

	// Lock file should be gone — the defer release ran.
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("lock file persists after graceful shutdown: %v", err)
	}
}

// harvestDeadPID spawns a short-lived subprocess, kills it,
// reaps it, and returns its now-dead PID. Used to plant orphan
// locks that PID-aware acquire will reliably detect as dead.
func harvestDeadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleep: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return pid
}

// buildCanopy compiles the canopy binary into a t.TempDir and
// returns the path. Cached across tests via the build-cache;
// repeat tests in the same `go test` run share the artifact.
func buildCanopy(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "canopy")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build canopy: %v", err)
	}
	return out
}

// setupSmokeFixture creates a tiny git-backed BCR mirror so
// `bzlhub sync run --interval` has a real target.
func setupSmokeFixture(t *testing.T) (mirrorPath, dbPath string) {
	t.Helper()
	remoteDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(remoteDir, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "master"},
		{"config", "user.email", "t@x"},
		{"config", "user.name", "tester"},
		{"add", "-A"},
		{"commit", "-q", "-m", "seed"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = remoteDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}

	mirror := filepath.Join(t.TempDir(), "mirror")
	db := filepath.Join(t.TempDir(), "bzlhub.db")

	// Bootstrap so the mirror exists.
	bin := buildCanopy(t) // reuses build cache
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "sync", "bootstrap",
		"--remote=file://"+remoteDir,
		"--mirror="+mirror,
		"--db="+db,
		"--branch=master")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bootstrap: %v (%s)", err, out)
	}
	return mirror, db
}
