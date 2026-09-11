package main

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// The desktop app cannot log its own segfault, so a bug report has no way to
// say whether the last run crashed or the user simply quit — the journal shows
// a process that stopped either way.
//
// Each run creates a file named after its PID and holds an exclusive lock on
// it for its whole lifetime. The lock is what carries the signal, not the file:
// the kernel releases it when the process dies, however it dies, so a marker
// we can lock belonged to a run that is gone. A marker we cannot lock belongs
// to an instance that is still alive.
//
// Liveness is deliberately not inferred from the PID. PIDs are reused, and a
// recycled one would make a crashed run look like a running second window —
// suppressing a real crash and reporting a window that does not exist.
//
// One file per PID rather than a single shared one: a second window must never
// adopt or delete the first one's marker, or quitting either would erase the
// other's crash evidence.
//
// Markers are scoped per host. A home directory can be shared across machines,
// and a lock taken on one host says nothing about a process on another — a
// flat directory would let one machine report a session running happily
// elsewhere as a crash.
func sessionDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".radar", "desktop-sessions", hostSlug())
}

func hostSlug() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown-host"
	}
	// Keep it a single safe path element regardless of what the OS reports.
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, name)
	if name = strings.Trim(name, "."); name == "" {
		return "unknown-host"
	}
	return name
}

// runningMarker is written under the lock once a run is fully recorded. A
// marker without it is either mid-creation or already released, and neither is
// a crash — the file existing is not enough, because it is briefly visible and
// unlocked at both ends of a run.
const runningMarker = "running"

// held keeps this run's marker open. Closing the file would drop the lock and
// advertise the process as gone while it is still running.
//
// Guarded because release is reachable from two goroutines: the Wails shutdown
// callback, and the self-update relaunch. Closing the window while a relaunch
// is pending runs both.
var (
	sessionMu sync.Mutex
	held      *os.File
)

func markSessionStart() { claimSession(sessionDir()) }
func markSessionEnd()   { releaseSession(sessionDir()) }

// claimSession reports any run that ended without cleaning up, then records
// this one. It must be called only once the process is committed to running:
// claiming before the startup checks would leave a marker behind on every
// os.Exit and report the next launch as a crash that never happened.
func claimSession(dir string) {
	sessionMu.Lock()
	defer sessionMu.Unlock()

	if dir == "" || held != nil {
		return
	}

	// Scan before claiming, so this run's own marker is never mistaken for an
	// abandoned one.
	reportAbandonedSessions(dir)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[desktop] could not record session marker: %v", err)
		return
	}

	path := sessionFile(dir, os.Getpid())
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		log.Printf("[desktop] could not record session marker: %v", err)
		return
	}
	locked, err := tryLockFile(file)
	if err != nil || !locked {
		// Without the lock the marker would claim this run had already ended.
		if err != nil {
			log.Printf("[desktop] could not lock session marker: %v", err)
		}
		file.Close()
		_ = os.Remove(path)
		return
	}

	// Only now, holding the lock, does the marker mean "a run is in progress".
	if _, err := file.WriteString(runningMarker); err != nil {
		log.Printf("[desktop] could not record session marker: %v", err)
		file.Close()
		_ = os.Remove(path)
		return
	}
	held = file
}

// releaseSession drops this run's marker so a deliberate exit is not reported
// as a crash. Other instances' markers are left alone.
func releaseSession(dir string) {
	sessionMu.Lock()
	defer sessionMu.Unlock()

	if dir == "" || held == nil {
		return
	}
	// Clear the marker while the lock is still held. Unlinking first would
	// leave the path briefly present and lockable, and a launch landing in
	// that gap would report this deliberate quit as a crash.
	if err := held.Truncate(0); err != nil {
		log.Printf("[desktop] could not clear session marker: %v", err)
	}
	held.Close() // releases the lock
	held = nil
	if err := os.Remove(sessionFile(dir, os.Getpid())); err != nil && !os.IsNotExist(err) {
		log.Printf("[desktop] could not clear session marker: %v", err)
	}
}

// reportAbandonedSessions logs every marker whose owner is gone and clears it,
// so one crash is reported once rather than on every launch afterwards.
func reportAbandonedSessions(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		file, err := os.OpenFile(path, os.O_RDWR, 0o600)
		if err != nil {
			continue
		}
		locked, err := tryLockFile(file)
		if err != nil || !locked {
			// Held by a live instance, or the filesystem cannot lock. Saying
			// nothing beats guessing at a crash that may not have happened.
			file.Close()
			continue
		}

		// Lockable and still marked running: the owner died without clearing
		// it. Anything else is a released or half-written marker, which is
		// swept away without a claim in either direction.
		if markedRunning(file) {
			log.Printf("[desktop] previous run (pid %d) did not exit cleanly — it crashed or was force-quit", pid)
		}
		file.Close()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("[desktop] could not clear stale session marker: %v", err)
		}
	}
}

func sessionFile(dir string, pid int) string {
	return filepath.Join(dir, strconv.Itoa(pid))
}

// markedRunning reports whether a marker was fully recorded by a run that then
// never released it.
func markedRunning(f *os.File) bool {
	buf := make([]byte, len(runningMarker))
	n, err := f.ReadAt(buf, 0)
	if err != nil && n != len(runningMarker) {
		return false
	}
	return string(buf[:n]) == runningMarker
}
