package podlogs

import (
	"testing"
	"time"
)

func cursorLine(follow int, ts, content string) Entry {
	return Entry{Timestamp: ts, Content: content, Follow: follow}
}

func TestStreamCursorResumesWithoutReplay(t *testing.T) {
	cursors := map[string]*StreamCursor{}
	c := CursorFor(cursors, "pg-1/postgres", "uid-1", 1)
	first := c.RestartOptions("postgres", 200, nil)
	if first.TailLines == nil || *first.TailLines != 200 || first.SinceTime != nil || !first.Follow || !first.Timestamps || first.Container != "postgres" {
		t.Fatalf("first start = %+v", first)
	}
	for _, e := range []Entry{cursorLine(1, "2026-09-28T14:00:00.1Z", "a"), cursorLine(1, "2026-09-28T14:00:05.7Z", "b"), cursorLine(1, "2026-09-28T14:00:05.7Z", "c")} {
		if !c.Admit(e) {
			t.Fatalf("fresh line %+v rejected", e)
		}
	}

	if CursorFor(cursors, "pg-1/postgres", "uid-1", 2) != c {
		t.Fatal("same Pod got a new cursor")
	}
	restart := c.RestartOptions("postgres", 200, nil)
	if restart.TailLines != nil || restart.SinceSeconds != nil || restart.SinceTime == nil ||
		!restart.SinceTime.Time.Equal(time.Date(2026, 9, 28, 14, 0, 5, 0, time.UTC)) {
		t.Fatalf("restart = %+v, want sinceTime at the last delivered second and no tail", restart)
	}

	// The resumed follow replays the boundary second.
	for _, e := range []Entry{cursorLine(2, "2026-09-28T14:00:05.2Z", "earlier in the second"), cursorLine(2, "2026-09-28T14:00:05.7Z", "b"), cursorLine(2, "2026-09-28T14:00:05.7Z", "c")} {
		if c.Admit(e) {
			t.Errorf("replayed line %+v admitted", e)
		}
	}
	for _, e := range []Entry{cursorLine(2, "2026-09-28T14:00:05.7Z", "d"), cursorLine(2, "2026-09-28T14:00:06Z", "e")} {
		if !c.Admit(e) {
			t.Errorf("new line %+v rejected", e)
		}
	}
	if c.Admit(cursorLine(2, "2026-09-28T14:00:05.7Z", "d")) {
		t.Error("line before the new last timestamp admitted")
	}
}

// Some runtimes stamp lines written together with one timestamp, so the same
// (timestamp, content) can legitimately arrive more than once.
func TestStreamCursorKeepsIdenticalLines(t *testing.T) {
	c := CursorFor(map[string]*StreamCursor{}, "web-0/app", "uid-1", 1)
	ts := "2026-09-28T14:00:05.7Z"
	for i := range 3 {
		if !c.Admit(cursorLine(1, ts, "")) {
			t.Fatalf("identical line %d in one follow rejected", i+1)
		}
	}
	// A resumed follow reads the three again, then a fourth that is new.
	for i := range 3 {
		if c.Admit(cursorLine(2, ts, "")) {
			t.Fatalf("replayed line %d admitted", i+1)
		}
	}
	if !c.Admit(cursorLine(2, ts, "")) {
		t.Fatal("fourth identical line rejected")
	}
}

// A replacement Pod can take the old one's name between discovery passes. It
// must start from the caller's window, and the old Pod's lines still queued
// must neither be dropped nor move the new cursor.
func TestStreamCursorReplacementPod(t *testing.T) {
	cursors := map[string]*StreamCursor{}
	old := CursorFor(cursors, "web-0/app", "uid-old", 1)
	oldLine := cursorLine(1, "2026-09-28T14:00:09Z", "old pod line")
	old.Admit(oldLine)
	if !old.FromCurrentPod(oldLine) {
		t.Fatal("the original cursor lost its Pod identity")
	}

	c := CursorFor(cursors, "web-0/app", "uid-new", 2)
	if c == old {
		t.Fatal("replacement Pod reused the old cursor")
	}
	if c.FromCurrentPod(oldLine) {
		t.Fatal("queued old lines must not certify the replacement Pod is readable")
	}
	if !c.FromCurrentPod(cursorLine(2, "2026-09-28T14:00:08Z", "new pod line")) {
		t.Fatal("replacement lines lost their Pod identity")
	}
	if opts := c.RestartOptions("app", 50, nil); opts.SinceTime != nil || opts.TailLines == nil {
		t.Fatalf("replacement start = %+v, want the caller's tail", opts)
	}
	if !c.Admit(cursorLine(1, "2026-09-28T14:00:10Z", "queued old pod line")) {
		t.Fatal("old Pod's queued line dropped")
	}
	if !c.Admit(cursorLine(2, "2026-09-28T14:00:08Z", "new pod line, node clock behind")) {
		t.Fatal("replacement Pod's line dropped because of the old Pod's timestamps")
	}
}
