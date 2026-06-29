package health

import "testing"

func TestRankOrdering(t *testing.T) {
	// neutral ties healthy at the bottom; unknown sits just above; then degraded,
	// then unhealthy. This is the single ordering all consumer rank maps derive
	// from — pinning it here is what keeps them from re-diverging.
	if Rank(LevelHealthy) != 0 || Rank(LevelNeutral) != 0 {
		t.Fatalf("healthy and neutral must both rank 0 (most-benign), got %d/%d", Rank(LevelHealthy), Rank(LevelNeutral))
	}
	if !(Rank(LevelHealthy) < Rank(LevelUnknown) &&
		Rank(LevelUnknown) < Rank(LevelDegraded) &&
		Rank(LevelDegraded) < Rank(LevelUnhealthy)) {
		t.Fatalf("rank order broken: healthy=%d unknown=%d degraded=%d unhealthy=%d",
			Rank(LevelHealthy), Rank(LevelUnknown), Rank(LevelDegraded), Rank(LevelUnhealthy))
	}
	// Unrecognized vocab maps to the unknown rank, not promoted to healthy.
	if Rank(Level("bogus")) != Rank(LevelUnknown) {
		t.Fatalf("unrecognized level should rank as unknown, got %d", Rank(Level("bogus")))
	}
}

func TestWorseOf(t *testing.T) {
	cases := []struct {
		a, b, want Level
	}{
		{LevelHealthy, LevelUnhealthy, LevelUnhealthy},
		{LevelDegraded, LevelHealthy, LevelDegraded},
		{LevelNeutral, LevelHealthy, LevelHealthy}, // tie → healthy, commutatively
		{LevelHealthy, LevelNeutral, LevelHealthy}, // tie → healthy, commutatively
		{LevelNeutral, LevelNeutral, LevelNeutral}, // all-neutral → neutral
		{LevelUnknown, LevelHealthy, LevelUnknown}, // unknown beats healthy
		{LevelNeutral, LevelDegraded, LevelDegraded},
		{"", LevelDegraded, LevelDegraded}, // empty = no opinion
		{LevelDegraded, "", LevelDegraded},
		{LevelUnhealthy, LevelDegraded, LevelUnhealthy},
	}
	for _, c := range cases {
		if got := WorseOf(c.a, c.b); got != c.want {
			t.Errorf("WorseOf(%q,%q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestLegacyString(t *testing.T) {
	cases := []struct {
		level Level
		want  string
	}{
		{LevelHealthy, "healthy"},
		{LevelNeutral, "healthy"},   // Succeeded etc. read healthy in legacy vocab
		{LevelUnknown, "healthy"},   // legacy classifier never produced unknown
		{LevelDegraded, "warning"},
		{LevelUnhealthy, "error"},
	}
	for _, c := range cases {
		if got := (Verdict{Level: c.level}).LegacyString(); got != c.want {
			t.Errorf("Verdict{%q}.LegacyString() = %q, want %q", c.level, got, c.want)
		}
	}
}
