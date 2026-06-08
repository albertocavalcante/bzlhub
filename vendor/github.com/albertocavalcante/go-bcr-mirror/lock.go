package bcrmirror

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const lockFileName = "canopy.lock"

// acquireMirrorLock takes an exclusive on-disk lock under
// <mirror>/.git/canopy.lock. Writes the current PID into the file
// so a future invocation can detect orphan locks left by crashed
// holders.
//
// The lock contract:
//
//   - First-time acquire (no existing lock): O_EXCL succeeds; we
//     write our PID and own the lock.
//   - Existing lock with a parseable, LIVE PID: ErrLocked.
//   - Existing lock with a parseable, DEAD PID: atomic takeover
//     via tmp + rename. Race-detected by reading the lock back
//     and verifying our PID won.
//   - Existing lock with unparseable content: ErrLocked. Refusing
//     is safer than guessing — an operator hand-edited file
//     deserves a manual investigation, not silent removal.
//
// Release is symmetric — only removes the lock if it still
// contains our PID. Protects against the "we got stolen mid-
// execution, then resurrected" scenario from deleting whoever
// legitimately took over.
//
// PID-aliveness uses syscall.Signal(0), the POSIX
// "does-this-process-exist" probe. Behavior on Windows differs;
// the takeover code path is best-effort there.
func acquireMirrorLock(mirrorPath string) (release func(), err error) {
	dir := filepath.Join(mirrorPath, ".git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("acquireMirrorLock: %w", err)
	}
	path := filepath.Join(dir, lockFileName)
	ourPID := os.Getpid()
	ourContent := []byte(strconv.Itoa(ourPID))

	// Fast path: nobody holds the lock.
	if release, err := tryCreateLock(path, ourContent, ourPID); err == nil {
		return release, nil
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}

	// Lock exists — investigate.
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil, fmt.Errorf("%w: %s (existing lock unreadable: %v)", ErrLocked, path, readErr)
	}
	pidStr := strings.TrimSpace(string(data))
	pid, parseErr := strconv.Atoi(pidStr)
	if parseErr != nil || pid <= 0 {
		return nil, fmt.Errorf("%w: %s (unparseable PID %q)", ErrLocked, path, pidStr)
	}
	if processIsAlive(pid) {
		return nil, fmt.Errorf("%w: %s (held by PID %d)", ErrLocked, path, pid)
	}

	// Holder is dead — take over via atomic rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, ourContent, 0o644); err != nil {
		return nil, fmt.Errorf("acquireMirrorLock takeover write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("acquireMirrorLock takeover rename: %w", err)
	}
	// Race verification: another recoverer may have raced us and
	// won. Read back; if the file doesn't match our PID, treat as
	// ErrLocked.
	final, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s (takeover verification read failed: %v)", ErrLocked, path, err)
	}
	if strings.TrimSpace(string(final)) != strconv.Itoa(ourPID) {
		return nil, fmt.Errorf("%w: %s (lost takeover race)", ErrLocked, path)
	}
	// First (and only) library log site: an orphan-lock recovery
	// happened. Operators investigating "why does canopy.lock have
	// a new mtime" get the dead PID + path inline.
	slog.Warn("bcrmirror: took over orphan lock",
		"dead_pid", pid,
		"path", path)
	return symmetricRelease(path, ourContent), nil
}

// tryCreateLock attempts the O_EXCL create. Returns the release
// func on success, or os.ErrExist (wrapped) if the file already
// exists, or another error for write failures.
func tryCreateLock(path string, content []byte, ourPID int) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if _, werr := f.Write(content); werr != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write lock: %w", werr)
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close lock: %w", cerr)
	}
	return symmetricRelease(path, content), nil
}

// symmetricRelease removes the lock only if it still contains our
// PID. Otherwise leaves the file alone — the lock has been taken
// over and we'd be stealing a healthy holder's lock.
func symmetricRelease(path string, ourContent []byte) func() {
	return func() {
		current, err := os.ReadFile(path)
		if err != nil {
			return
		}
		if strings.TrimSpace(string(current)) == strings.TrimSpace(string(ourContent)) {
			_ = os.Remove(path)
		}
	}
}

// processIsAlive tests whether pid is a live process via POSIX
// Signal(0) (the "is process X alive?" probe — sends no signal,
// just checks if signalling would succeed). On Windows os.FindProcess
// always succeeds and Signal is a no-op; the function returns true
// there, which means takeover is disabled on Windows.
func processIsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		// ESRCH = No such process; EPERM = process exists but we
		// can't signal it (still alive, just not ours).
		return errors.Is(err, syscall.EPERM)
	}
	return true
}
