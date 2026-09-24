// Package poll holds the in-product "help us improve Radar OSS" poll: the
// round being asked, who is eligible to see it, and how answers are validated
// before they leave the machine.
package poll

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"time"
)

// The same file the web UI renders from. round_test.go fails if the two copies
// drift, so the server never accepts ids the UI doesn't offer, or rejects ones
// it does.
//
//go:embed round.json
var roundJSON []byte

type Option struct {
	ID       string `json:"id"`
	Requires string `json:"requires,omitempty"`
}

type Condition struct {
	Question string   `json:"question"`
	AnyOf    []string `json:"anyOf"`
	Record   string   `json:"record,omitempty"`
}

type Question struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Max        int        `json:"max,omitempty"`
	Mode       string     `json:"mode,omitempty"`
	Block      string     `json:"block,omitempty"`
	TextOption string     `json:"textOption,omitempty"`
	TextAlways bool       `json:"textAlways,omitempty"`
	Exclusive  string     `json:"exclusive,omitempty"`
	ShowIf     *Condition `json:"showIf,omitempty"`
	SkipIf     *Condition `json:"skipIf,omitempty"`
	Options    []Option   `json:"options"`
}

type Round struct {
	ID        string     `json:"id"`
	Block     string     `json:"block"`
	StartsAt  time.Time  `json:"startsAt"`
	EndsAt    time.Time  `json:"endsAt"`
	Questions []Question `json:"questions"`
}

// Active reports whether the round is open at now. A binary that outlives its
// round goes quiet on its own; there is no remote switch.
func (r Round) Active(now time.Time) bool {
	return !now.Before(r.StartsAt) && now.Before(r.EndsAt)
}

// Current is the round compiled into this binary.
var Current = mustParseRound(roundJSON)

func mustParseRound(data []byte) Round {
	r, err := parseRound(data)
	if err != nil {
		panic(err)
	}
	return r
}

func parseRound(data []byte) (Round, error) {
	var r Round
	if err := json.Unmarshal(data, &r); err != nil {
		return Round{}, fmt.Errorf("parse poll round: %w", err)
	}
	if r.ID == "" || r.StartsAt.IsZero() || !r.EndsAt.After(r.StartsAt) || len(r.Questions) == 0 {
		return Round{}, fmt.Errorf("poll round %q is incomplete", r.ID)
	}
	return r, nil
}

func (r Round) question(id string) (Question, bool) {
	for _, q := range r.Questions {
		if q.ID == id {
			return q, true
		}
	}
	return Question{}, false
}

func (q Question) hasOption(id string) bool {
	for _, o := range q.Options {
		if o.ID == id {
			return true
		}
	}
	return false
}
