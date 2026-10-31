// Package launchday holds the window of Radar's Product Hunt launch, when
// Radar's own prompts step aside for the launch notice. The update check can
// move or cancel it; until it does, the built-in window applies.
package launchday

import (
	"fmt"
	"net/url"
	"sync"
	"time"
)

// Launch is the Product Hunt launch as the update check describes it.
type Launch struct {
	Enabled bool      `json:"enabled"`
	URL     string    `json:"url"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
}

// Product Hunt's day runs midnight to midnight Pacific; PDT is UTC-7 until Nov 1.
var builtin = Launch{
	Enabled: true,
	URL:     "https://www.producthunt.com/products/radar-7?launch=radar-101",
	Start:   time.Date(2026, time.October, 29, 7, 0, 0, 0, time.UTC),
	End:     time.Date(2026, time.October, 30, 7, 0, 0, 0, time.UTC),
}

var (
	mu         sync.RWMutex
	fromServer *Launch
)

// Set records the launch the update check returned. It is kept until a later
// check replaces it, so one failed check does not bring back the built-in window.
func Set(l Launch) error {
	if l.Enabled {
		u, err := url.Parse(l.URL)
		if err != nil || u.Scheme != "https" || (u.Host != "www.producthunt.com" && u.Host != "producthunt.com") {
			return fmt.Errorf("launch URL %q is not a Product Hunt link", l.URL)
		}
		if !l.End.After(l.Start) {
			return fmt.Errorf("launch window ends (%s) before it starts (%s)", l.End, l.Start)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	fromServer = &l
	return nil
}

// Current returns the launch from the latest update check, or the built-in one.
func Current() Launch {
	mu.RLock()
	defer mu.RUnlock()
	if fromServer != nil {
		return *fromServer
	}
	return builtin
}

// ProductHunt reports whether t falls on the Product Hunt launch day.
func ProductHunt(t time.Time) bool {
	l := Current()
	return l.Enabled && !t.Before(l.Start) && t.Before(l.End)
}
