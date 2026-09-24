package poll

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func TestRoundMatchesWebCopy(t *testing.T) {
	web, err := os.ReadFile("../../web/src/components/poll/round-2026-q4.json")
	if err != nil {
		t.Fatalf("read web round: %v", err)
	}
	if !bytes.Equal(web, roundJSON) {
		t.Fatal("internal/poll/round.json differs from web/src/components/poll/round-2026-q4.json; copy the web file over")
	}
}

func TestCurrentRoundWindow(t *testing.T) {
	if Current.EndsAt.Sub(Current.StartsAt) != 120*24*time.Hour {
		t.Fatalf("round lasts %v, want 120 days", Current.EndsAt.Sub(Current.StartsAt))
	}
	if Current.Active(Current.StartsAt.Add(-time.Second)) || !Current.Active(Current.StartsAt) || Current.Active(Current.EndsAt) {
		t.Fatal("Active must be [StartsAt, EndsAt)")
	}
}

func TestParseRoundRejectsIncomplete(t *testing.T) {
	for _, data := range []string{`{}`, `{"id":"x","startsAt":"2026-01-02T00:00:00Z","endsAt":"2026-01-01T00:00:00Z","questions":[{"id":"q"}]}`, `not json`} {
		if _, err := parseRound([]byte(data)); err == nil {
			t.Errorf("parseRound(%s) succeeded, want error", data)
		}
	}
}
