package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestReplayStream(t *testing.T) {
	const input = `{"at":"2026-01-08T00:00:00Z","workload":"Deployment/example","replicas":1,"row":{"container":"app","resource":"cpu","fit":"insufficient_history","decision":{"status":"none"}}}`
	var out bytes.Buffer
	if err := replay(strings.NewReader(input+"\n"+input), &out, 2); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two reports, got %d", len(lines))
	}
	var report comparison
	if err := json.Unmarshal([]byte(lines[0]), &report); err != nil {
		t.Fatal(err)
	}
	if report.Workload != "Deployment/example" || report.MinDays != 2 || report.FlatHalf != nil || report.Direct != nil {
		t.Fatalf("unexpected report for a row with insufficient history: %+v", report)
	}

	if err := replay(strings.NewReader(input+"\n{"), &out, 2); err == nil || !strings.Contains(err.Error(), "case 2") {
		t.Fatalf("missing partial-input error: %v", err)
	}
}

func TestReplayInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		input string
		days  int
	}{
		{"", 2},
		{"{}", 2},
		{`{"at":"2026-01-08T00:00:00Z","workload":"w","row":{"container":"c","resource":"cpu"}}`, 2},
		{`{"at":"2026-01-08T00:00:00Z","workload":"w","row":{"container":"c","resource":"cpu","decision":{"status":"none"}},"evidence":{"source_time":[]}}`, 2},
		{"{}", 0},
		{"{}", 8},
		{`{"at":"2026-01-08T00:00:00Z","workload":"example","row":{"container":"app","resource":"disk"}}`, 2},
	} {
		if err := replay(strings.NewReader(tc.input), &bytes.Buffer{}, tc.days); err == nil {
			t.Errorf("accepted invalid input %q with %d days", tc.input, tc.days)
		}
	}
}

func TestReplayEligibleAlternatives(t *testing.T) {
	for _, tt := range []struct {
		req          string
		observed     float64
		days         int
		status       string
		direct, half float64
	}{
		{"1", .17, 3, "cut", 200, 500},
		{"15m", 0, 1, "none", 10, 10},
	} {
		c := shadowCase("cpu", tt.req, tt.observed, tt.days)
		var input, output bytes.Buffer
		if err := json.NewEncoder(&input).Encode(c); err != nil {
			t.Fatal(err)
		}
		if err := replay(&input, &output, 1); err != nil {
			t.Fatal(err)
		}
		var got comparison
		if err := json.NewDecoder(&output).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.Candidate.Status != tt.status || got.Direct == nil || *got.Direct != tt.direct || got.FlatHalf == nil || *got.FlatHalf != tt.half || got.BaselineRow.Decision.Status != c.Row.Decision.Status {
			t.Fatalf("%s D=%d: %+v", tt.req, tt.days, got)
		}
		if tt.status == "cut" && (got.Candidate.Next == nil || *got.Candidate.Next != 658 || got.BaselineVisibleSavings != 500 || got.ConfigSince == nil || got.BaselineRow.Observed == nil) {
			t.Fatalf("next: %+v", got.Candidate)
		}
	}
}
