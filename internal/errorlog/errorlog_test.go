package errorlog

import (
	"fmt"
	"testing"
)

func TestRecordAndGetEntries(t *testing.T) {
	Reset()

	Record("test", "error", "something broke: %s", "details")
	Record("test", "warning", "heads up")

	entries := GetEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Source != "test" || entries[0].Level != "error" {
		t.Errorf("entry 0: got source=%q level=%q", entries[0].Source, entries[0].Level)
	}
	if entries[0].Message != "something broke: details" {
		t.Errorf("entry 0 message: got %q", entries[0].Message)
	}
	if entries[1].Level != "warning" {
		t.Errorf("entry 1 level: got %q", entries[1].Level)
	}
}

func TestRingBufferOverflow(t *testing.T) {
	Reset()

	for i := 0; i < maxEntries+50; i++ {
		Record("test", "error", "msg %d", i)
	}

	entries := GetEntries()
	if len(entries) != maxEntries {
		t.Fatalf("expected %d entries, got %d", maxEntries, len(entries))
	}

	// Oldest entry should be #50 (0-49 evicted)
	want := fmt.Sprintf("msg %d", 50)
	if entries[0].Message != want {
		t.Errorf("oldest entry: got %q, want %q", entries[0].Message, want)
	}

	// Newest entry should be the last one written
	wantLast := fmt.Sprintf("msg %d", maxEntries+49)
	if entries[len(entries)-1].Message != wantLast {
		t.Errorf("newest entry: got %q, want %q", entries[len(entries)-1].Message, wantLast)
	}
}

func TestCount(t *testing.T) {
	Reset()

	if Count() != 0 {
		t.Fatalf("expected 0, got %d", Count())
	}

	Record("a", "error", "x")
	Record("b", "warning", "y")

	if Count() != 2 {
		t.Fatalf("expected 2, got %d", Count())
	}
}

func TestGetEntriesIsDeepCopy(t *testing.T) {
	Reset()

	Record("test", "error", "original")
	entries := GetEntries()

	// Mutate the copy
	entries[0].Message = "modified"

	// Original should be unchanged
	fresh := GetEntries()
	if fresh[0].Message != "original" {
		t.Errorf("GetEntries returned a shallow copy: message was %q", fresh[0].Message)
	}
}
