package errorlog

import (
	"fmt"
	"sync"
	"time"
)

const maxEntries = 200

// ErrorEntry represents a single recorded error.
type ErrorEntry struct {
	Time    time.Time `json:"time"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
	Level   string    `json:"level"` // "error" or "warning"
}

// ErrorLog is a bounded ring buffer of recent backend errors.
type ErrorLog struct {
	mu            sync.RWMutex
	entries       []ErrorEntry
	totalRecorded int64
}

var (
	global     *ErrorLog
	globalOnce sync.Once
)

func instance() *ErrorLog {
	globalOnce.Do(func() {
		global = &ErrorLog{
			entries: make([]ErrorEntry, 0, maxEntries),
		}
	})
	return global
}

// Record adds an error entry to the global ring buffer.
// source is the subsystem name (e.g. "prometheus", "helm", "metrics", "copy").
// level is "error" or "warning".
func Record(source, level, format string, args ...any) {
	l := instance()
	entry := ErrorEntry{
		Time:    time.Now(),
		Source:  source,
		Message: fmt.Sprintf(format, args...),
		Level:   level,
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.totalRecorded++

	if len(l.entries) >= maxEntries {
		copy(l.entries, l.entries[1:])
		l.entries[len(l.entries)-1] = entry
	} else {
		l.entries = append(l.entries, entry)
	}
}

// GetEntries returns a deep copy of all recorded entries.
func GetEntries() []ErrorEntry {
	l := instance()
	l.mu.RLock()
	defer l.mu.RUnlock()

	out := make([]ErrorEntry, len(l.entries))
	copy(out, l.entries)
	return out
}

// Count returns the number of recorded entries.
func Count() int {
	l := instance()
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.entries)
}

// TotalRecorded returns the total number of errors ever recorded (including evicted ones).
func TotalRecorded() int64 {
	l := instance()
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.totalRecorded
}

// Reset clears all entries (useful for testing).
func Reset() {
	l := instance()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = l.entries[:0]
	l.totalRecorded = 0
}
