package cliui

import (
	"bytes"
	"testing"
)

func TestColorAllowed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tty     bool
		noColor string
		term    string
		want    bool
	}{
		{name: "interactive", tty: true, term: "xterm-256color", want: true},
		{name: "not a terminal", term: "xterm-256color"},
		{name: "NO_COLOR", tty: true, noColor: "1", term: "xterm-256color"},
		{name: "NO_COLOR zero still disables", tty: true, noColor: "0", term: "xterm-256color"},
		{name: "dumb terminal", tty: true, term: "dumb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := colorAllowed(tc.tty, tc.noColor, tc.term); got != tc.want {
				t.Fatalf("colorAllowed() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestStylerUsesSemanticMarkersWithoutChangingPlainOutput(t *testing.T) {
	plain := New(&bytes.Buffer{})
	if got := plain.Marker(Success) + " " + plain.Tone(Success, "connected"); got != "✓ connected" {
		t.Fatalf("plain state = %q", got)
	}

	styled := Styler{Enabled: true}
	if got := styled.Marker(Failure); got != Red+"✗"+Reset {
		t.Fatalf("failure marker = %q", got)
	}
	if got := styled.Tone(Attention, "not checked"); got != Amber+"not checked"+Reset {
		t.Fatalf("attention text = %q", got)
	}
	if got := styled.Marker(Neutral); got != "○" {
		t.Fatalf("neutral marker = %q", got)
	}
}
