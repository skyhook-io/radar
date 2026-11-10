package launchday

import (
	"testing"
	"time"
)

func reset() {
	mu.Lock()
	fromServer = nil
	mu.Unlock()
}

func TestProductHunt(t *testing.T) {
	t.Cleanup(reset)
	pdt := time.FixedZone("PDT", -7*60*60)
	cases := []struct {
		at   time.Time
		want bool
	}{
		{time.Date(2026, time.October, 28, 23, 59, 59, 0, pdt), false},
		{time.Date(2026, time.October, 29, 0, 0, 0, 0, pdt), true},
		{time.Date(2026, time.October, 29, 23, 59, 59, 0, pdt), true},
		{time.Date(2026, time.October, 30, 0, 0, 0, 0, pdt), false},
	}
	for _, c := range cases {
		if got := ProductHunt(c.at); got != c.want {
			t.Errorf("ProductHunt(%s) = %v, want %v", c.at, got, c.want)
		}
	}
}

func TestSetMovesOrCancelsTheLaunch(t *testing.T) {
	t.Cleanup(reset)
	oct29 := time.Date(2026, time.October, 29, 12, 0, 0, 0, time.UTC)
	nov5 := time.Date(2026, time.November, 5, 12, 0, 0, 0, time.UTC)

	moved := Launch{Enabled: true, URL: builtin.URL, Start: nov5.Add(-5 * time.Hour), End: nov5.Add(19 * time.Hour)}
	if err := Set(moved); err != nil {
		t.Fatal(err)
	}
	if ProductHunt(oct29) || !ProductHunt(nov5) {
		t.Error("a moved launch should apply on the new day only")
	}

	if err := Set(Launch{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if ProductHunt(nov5) || ProductHunt(oct29) {
		t.Error("a cancelled launch should never apply")
	}
}

func TestSetRejectsBadLaunches(t *testing.T) {
	t.Cleanup(reset)
	bad := []Launch{
		{Enabled: true, URL: "https://example.com/x", Start: builtin.Start, End: builtin.End},
		{Enabled: true, URL: "http://www.producthunt.com/x", Start: builtin.Start, End: builtin.End},
		{Enabled: true, URL: builtin.URL, Start: builtin.End, End: builtin.Start},
	}
	for _, l := range bad {
		if err := Set(l); err == nil {
			t.Errorf("Set(%+v) accepted a bad launch", l)
		}
	}
	if Current() != builtin {
		t.Error("a rejected launch should leave the built-in one in place")
	}
}
