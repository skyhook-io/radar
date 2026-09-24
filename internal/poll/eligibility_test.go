package poll

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func eligibleInputs() Inputs {
	now := Current.StartsAt.Add(24 * time.Hour)
	return Inputs{
		Now:         now,
		Round:       Current,
		Mode:        "local",
		InstalledAt: now.Add(-30 * 24 * time.Hour),
	}
}

func TestEligible(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Inputs)
		want   string
	}{
		{"eligible", func(*Inputs) {}, ""},
		{"RADAR_POLL=off", func(in *Inputs) { in.Disabled = true }, "disabled"},
		{"development build", func(in *Inputs) { in.DevBuild = true }, "development_build"},
		{"cloud mode", func(in *Inputs) { in.CloudMode = true }, "cloud"},
		{"--cloud-url without cloud mode", func(in *Inputs) { in.TunnelSet = true }, "cloud"},
		{"before the round", func(in *Inputs) { in.Now = Current.StartsAt.Add(-time.Hour) }, "no_active_round"},
		{"round expired", func(in *Inputs) { in.Now = Current.EndsAt; in.InstalledAt = in.Now.Add(-400 * 24 * time.Hour) }, "no_active_round"},
		{"install age unknown", func(in *Inputs) { in.InstalledAt = time.Time{} }, "install_age_unknown"},
		{"installed 13 days ago", func(in *Inputs) { in.InstalledAt = in.Now.Add(-13 * 24 * time.Hour) }, "too_new"},
		{"installed 15 days ago", func(in *Inputs) { in.InstalledAt = in.Now.Add(-15 * 24 * time.Hour) }, ""},
		{"star prompt yesterday", func(in *Inputs) { in.StarPromptedAt = in.Now.Add(-24 * time.Hour) }, "star_prompt_recent"},
		{"star prompt 3 days ago", func(in *Inputs) { in.StarPromptedAt = in.Now.Add(-72 * time.Hour) }, ""},
		{"don't ask again", func(in *Inputs) { in.State.NeverAt = in.Now.Add(-400 * 24 * time.Hour) }, "dismissed"},
		{"answered this round", func(in *Inputs) { in.State.SubmittedRound = Current.ID }, "submitted"},
		{"answered last round", func(in *Inputs) { in.State.SubmittedRound = "2026-q1" }, ""},
		{"shown 89 days ago", func(in *Inputs) { in.State.ShownAt = in.Now.Add(-89 * 24 * time.Hour) }, "shown_recently"},
		{"shown 91 days ago", func(in *Inputs) { in.State.ShownAt = in.Now.Add(-91 * 24 * time.Hour) }, ""},
		{"in-cluster ignores local state", func(in *Inputs) {
			in.Mode = "in-cluster"
			in.State.NeverAt = in.Now
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := eligibleInputs()
			tc.mutate(&in)
			ok, reason := Eligible(in)
			if reason != tc.want || ok != (tc.want == "") {
				t.Fatalf("Eligible = %v %q, want reason %q", ok, reason, tc.want)
			}
		})
	}
}

func TestAgeBucket(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for age, want := range map[time.Duration]string{
		10 * 24 * time.Hour:  "under_1_month",
		60 * 24 * time.Hour:  "1_6_months",
		365 * 24 * time.Hour: "over_6_months",
	} {
		if got := AgeBucket(now, now.Add(-age)); got != want {
			t.Errorf("AgeBucket(%v) = %q, want %q", age, got, want)
		}
	}
	if got := AgeBucket(now, time.Time{}); got != "unknown" {
		t.Errorf("zero install time = %q, want unknown", got)
	}
}

func TestStoreConcurrentUpdates(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "poll.json"))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); _ = s.Update(func(st *State) { st.ShownAt = time.Unix(1, 0) }) }()
		go func() { defer wg.Done(); _ = s.Update(func(st *State) { st.NeverAt = time.Unix(2, 0) }) }()
		go func() { defer wg.Done(); _ = s.Update(func(st *State) { st.SubmittedRound = "r" }) }()
	}
	wg.Wait()
	st := s.Load()
	if st.ShownAt.IsZero() || st.NeverAt.IsZero() || st.SubmittedRound != "r" {
		t.Fatalf("lost an update: %+v", st)
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(s.path), "*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}
