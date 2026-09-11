package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(flags)
	})
	fn()
	return buf.String()
}

// newSessionDir isolates each test and resets the process-wide marker handle,
// which claimSession would otherwise carry between tests.
func newSessionDir(t *testing.T) string {
	t.Helper()
	if held != nil {
		held.Close()
		held = nil
	}
	t.Cleanup(func() {
		if held != nil {
			held.Close()
			held = nil
		}
	})
	return filepath.Join(t.TempDir(), "desktop-sessions")
}

// abandonedMarker is what a crashed run leaves: a marker recorded as running,
// with no lock on it, because the kernel released the lock when the process
// died.
func abandonedMarker(t *testing.T, dir string, pid int) {
	t.Helper()
	writeMarker(t, dir, pid, runningMarker)
}

func writeMarker(t *testing.T, dir string, pid int, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("prepare session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)), []byte(content), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
}

// liveMarker is what a running instance leaves: a marker held under lock. The
// lock lives on this file handle, so a second attempt to take it fails the way
// it would across processes.
func liveMarker(t *testing.T, dir string, pid int) {
	t.Helper()
	abandonedMarker(t, dir, pid)
	file, err := os.OpenFile(filepath.Join(dir, strconv.Itoa(pid)), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open marker: %v", err)
	}
	locked, err := tryLockFile(file)
	if err != nil || !locked {
		file.Close()
		t.Skipf("filesystem does not support locking here (locked=%v err=%v)", locked, err)
	}
	t.Cleanup(func() { file.Close() })
}

func markerExists(t *testing.T, dir string, pid int) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, strconv.Itoa(pid)))
	return err == nil
}

func TestClaimSessionReportsUncleanExit(t *testing.T) {
	dir := newSessionDir(t)
	abandonedMarker(t, dir, 4242)

	out := captureLog(t, func() { claimSession(dir) })

	if !strings.Contains(out, "did not exit cleanly") {
		t.Errorf("log = %q, want an unclean-exit report", out)
	}
	if markerExists(t, dir, 4242) {
		t.Error("stale marker survived; the crash would be reported again on every later launch")
	}
	if !markerExists(t, dir, os.Getpid()) {
		t.Error("this run did not record a marker")
	}
}

// A crash must be reported once, not on every launch forever afterwards.
func TestClaimSessionReportsAnUncleanExitOnlyOnce(t *testing.T) {
	dir := newSessionDir(t)
	abandonedMarker(t, dir, 4242)

	captureLog(t, func() { claimSession(dir) })
	releaseSession(dir)
	out := captureLog(t, func() { claimSession(dir) })

	if strings.Contains(out, "did not exit cleanly") {
		t.Errorf("log = %q, want the crash reported only on the first launch after it", out)
	}
}

// A marker under lock belongs to a running instance. Claiming a crash there
// would fire every time someone opens a second window.
func TestClaimSessionIgnoresALiveInstance(t *testing.T) {
	dir := newSessionDir(t)
	liveMarker(t, dir, 4243)

	out := captureLog(t, func() { claimSession(dir) })

	if strings.Contains(out, "did not exit cleanly") {
		t.Errorf("log = %q, want no crash claim while another instance holds its marker", out)
	}
	if !markerExists(t, dir, 4243) {
		t.Error("a running instance's marker was removed; its crash would go unreported")
	}
}

// The whole point of locking rather than checking the PID: a recycled PID must
// not make a crashed run look like a running one. The marker is abandoned, so
// it is reported regardless of what that number now refers to.
func TestClaimSessionReportsCrashUnderAReusedPID(t *testing.T) {
	dir := newSessionDir(t)
	// A live, unrelated process holding this PID — represented here by the
	// only PID guaranteed to be alive during the test.
	abandonedMarker(t, dir, os.Getpid())

	out := captureLog(t, func() { claimSession(dir) })

	if !strings.Contains(out, "did not exit cleanly") {
		t.Errorf("log = %q, want the crash reported even though the PID is live", out)
	}
	if !markerExists(t, dir, os.Getpid()) {
		t.Error("this run did not record a marker after clearing the stale one")
	}
}

// Quitting one window must leave a concurrent instance's marker intact.
func TestReleaseSessionPreservesAnotherInstanceMarker(t *testing.T) {
	dir := newSessionDir(t)
	liveMarker(t, dir, 4244)
	claimSession(dir)

	releaseSession(dir)

	if markerExists(t, dir, os.Getpid()) {
		t.Error("own marker survived a clean shutdown")
	}
	if !markerExists(t, dir, 4244) {
		t.Error("a concurrent instance's marker was deleted by this instance quitting")
	}
}

func TestClaimSessionSilentOnFirstRun(t *testing.T) {
	dir := newSessionDir(t)

	out := captureLog(t, func() { claimSession(dir) })

	if out != "" {
		t.Errorf("log = %q, want silence when no previous run is recorded", out)
	}
	if !markerExists(t, dir, os.Getpid()) {
		t.Error("first run did not record a marker")
	}
}

// A clean shutdown must leave nothing behind, or every deliberate quit is
// reported as a crash on the next launch.
func TestReleaseSessionClearsOwnMarker(t *testing.T) {
	dir := newSessionDir(t)
	claimSession(dir)

	releaseSession(dir)

	if markerExists(t, dir, os.Getpid()) {
		t.Error("marker still present after shutdown")
	}
	out := captureLog(t, func() { claimSession(dir) })
	if strings.Contains(out, "did not exit cleanly") {
		t.Errorf("log = %q, want a clean shutdown to leave no crash report", out)
	}
}

// The marker only means anything while this run holds its lock. If the lock
// were dropped, a concurrent launch would read the run as already finished.
func TestClaimSessionHoldsTheLockForTheRun(t *testing.T) {
	dir := newSessionDir(t)
	claimSession(dir)

	other, err := os.OpenFile(sessionFile(dir, os.Getpid()), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open own marker: %v", err)
	}
	defer other.Close()

	locked, err := tryLockFile(other)
	if err != nil {
		t.Fatalf("lock attempt failed: %v", err)
	}
	if locked {
		t.Error("own marker was lockable mid-run; a concurrent launch would report this run as crashed")
	}
}

func TestClaimSessionIgnoresUnrelatedFiles(t *testing.T) {
	dir := newSessionDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("prepare session dir: %v", err)
	}
	for _, name := range []string{"not-a-pid", "-1", "0", ".hidden"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
	}

	out := captureLog(t, func() { claimSession(dir) })

	if out != "" {
		t.Errorf("log = %q, want non-PID entries ignored silently", out)
	}
	if !markerExists(t, dir, os.Getpid()) {
		t.Error("this run did not record a marker")
	}
}

func TestSessionDirectoryIsPrivate(t *testing.T) {
	dir := newSessionDir(t)
	claimSession(dir)

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat session dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("session dir mode = %o, want 700", perm)
	}
}

func TestSessionHelpersTolerateNoHomeDirectory(t *testing.T) {
	// sessionDir returns "" when the home directory is unknown; the helpers
	// must degrade to doing nothing rather than panicking.
	newSessionDir(t)
	claimSession("")
	releaseSession("")
}

// A run is briefly visible and unlocked at both ends of its life: after the
// file is created but before it is locked, and after the lock is dropped but
// before the file is unlinked. A launch landing in either gap can take the
// lock, so the file existing cannot be what marks a crash.
func TestClaimSessionIgnoresAMarkerLeftFromAReleasedRun(t *testing.T) {
	dir := newSessionDir(t)
	writeMarker(t, dir, 4245, "") // released: truncated before the lock was dropped

	out := captureLog(t, func() { claimSession(dir) })

	if strings.Contains(out, "did not exit cleanly") {
		t.Errorf("log = %q, want a deliberate quit not reported as a crash", out)
	}
	if markerExists(t, dir, 4245) {
		t.Error("released marker was left behind; it would accumulate forever")
	}
}

func TestClaimSessionIgnoresAHalfWrittenMarker(t *testing.T) {
	dir := newSessionDir(t)
	writeMarker(t, dir, 4246, "run") // created and locked, not yet fully recorded

	out := captureLog(t, func() { claimSession(dir) })

	if strings.Contains(out, "did not exit cleanly") {
		t.Errorf("log = %q, want a half-written marker not reported as a crash", out)
	}
}

// The marker only counts as a crash once the run is fully recorded, so the
// content has to be written while the lock is held.
func TestClaimSessionRecordsTheRunningMarkerUnderLock(t *testing.T) {
	dir := newSessionDir(t)
	claimSession(dir)

	data, err := os.ReadFile(sessionFile(dir, os.Getpid()))
	if err != nil {
		t.Fatalf("read own marker: %v", err)
	}
	if string(data) != runningMarker {
		t.Errorf("marker content = %q, want %q", data, runningMarker)
	}
}

// Simulates the release window directly: this run's own marker, cleared while
// still locked, must not read as a crash to the launch that picks it up.
func TestReleasedMarkerIsNotACrashEvenIfUnlinkFails(t *testing.T) {
	dir := newSessionDir(t)
	claimSession(dir)

	if err := held.Truncate(0); err != nil {
		t.Fatalf("truncate own marker: %v", err)
	}
	held.Close()
	held = nil
	// Deliberately skip the unlink, standing in for a failed os.Remove.

	out := captureLog(t, func() { claimSession(dir) })

	if strings.Contains(out, "did not exit cleanly") {
		t.Errorf("log = %q, want a cleared marker never reported as a crash", out)
	}
}

// A home directory can be shared across machines. A lock taken on one host
// says nothing about a process on another, so one machine must never read
// another's markers and report a live session as a crash.
func TestSessionDirIsScopedPerHost(t *testing.T) {
	dir := sessionDir()
	if dir == "" {
		t.Skip("no home directory available")
	}
	if filepath.Base(dir) != hostSlug() {
		t.Errorf("session dir = %q, want it scoped under host %q", dir, hostSlug())
	}
	if filepath.Base(filepath.Dir(dir)) != "desktop-sessions" {
		t.Errorf("session dir = %q, want it under desktop-sessions/<host>", dir)
	}
}

func TestHostSlugIsASafePathElement(t *testing.T) {
	slug := hostSlug()
	if slug == "" {
		t.Fatal("hostSlug() = empty, want a usable path element")
	}
	if strings.ContainsAny(slug, `/\`) || slug == "." || slug == ".." {
		t.Errorf("hostSlug() = %q, want a single safe path element", slug)
	}
}

// Release is reachable from the Wails shutdown callback and from the
// self-update relaunch goroutine; closing the window mid-relaunch runs both.
func TestSessionStateIsSafeUnderConcurrentRelease(t *testing.T) {
	dir := newSessionDir(t)
	claimSession(dir)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			releaseSession(dir)
		}()
	}
	wg.Wait()

	if markerExists(t, dir, os.Getpid()) {
		t.Error("marker survived concurrent release")
	}
}

// Claim and release can also overlap across goroutines without corrupting the
// handle or leaving a marker behind.
func TestSessionStateIsSafeUnderConcurrentClaimAndRelease(t *testing.T) {
	dir := newSessionDir(t)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); claimSession(dir) }()
		go func() { defer wg.Done(); releaseSession(dir) }()
	}
	wg.Wait()

	releaseSession(dir)
	if markerExists(t, dir, os.Getpid()) {
		t.Error("marker left behind after the final release")
	}
}
