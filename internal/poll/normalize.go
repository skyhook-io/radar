package poll

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxTextRunes caps every free-text answer.
const MaxTextRunes = 2000

// Answer is one question's response: chosen option ids, free text, or both
// where the question allows it.
type Answer struct {
	Choices []string `json:"choices,omitempty"`
	Text    string   `json:"text,omitempty"`
}

// Normalize validates answers against the round and returns only what this
// person could actually have been asked. Answers to questions hidden by their
// branch, mode or block are dropped rather than rejected, because going Back
// and changing an earlier answer legitimately strands them. Anything the UI
// could never produce is an error.
func (r Round) Normalize(answers map[string]Answer, mode string) (map[string]Answer, error) {
	for id := range answers {
		if _, ok := r.question(id); !ok {
			return nil, fmt.Errorf("unknown question %q", id)
		}
	}

	out := make(map[string]Answer, len(answers))
	for _, q := range r.Questions {
		if !r.visible(q, mode, out) {
			if q.SkipIf != nil && q.SkipIf.Record != "" && conditionMet(q.SkipIf, out) {
				out[q.ID] = Answer{Choices: []string{q.SkipIf.Record}}
			}
			continue
		}
		a, ok := answers[q.ID]
		if !ok {
			continue
		}
		clean, err := q.normalize(a)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", q.ID, err)
		}
		if len(clean.Choices) > 0 || clean.Text != "" {
			out[q.ID] = clean
		}
	}
	return out, nil
}

func (r Round) visible(q Question, mode string, answered map[string]Answer) bool {
	if q.Block != "" && q.Block != r.Block {
		return false
	}
	if q.Mode != "" && q.Mode != normalizeMode(mode) {
		return false
	}
	if q.ShowIf != nil && !conditionMet(q.ShowIf, answered) {
		return false
	}
	if q.SkipIf != nil && conditionMet(q.SkipIf, answered) {
		return false
	}
	return true
}

func normalizeMode(mode string) string {
	if mode == "in-cluster" {
		return "in-cluster"
	}
	return "local"
}

func conditionMet(c *Condition, answered map[string]Answer) bool {
	a, ok := answered[c.Question]
	if !ok {
		return false
	}
	for _, chosen := range a.Choices {
		for _, want := range c.AnyOf {
			if chosen == want {
				return true
			}
		}
	}
	return false
}

func (q Question) normalize(a Answer) (Answer, error) {
	text := strings.TrimSpace(a.Text)
	if utf8.RuneCountInString(text) > MaxTextRunes {
		return Answer{}, fmt.Errorf("text is longer than %d characters", MaxTextRunes)
	}

	if q.Kind == "text" {
		if len(a.Choices) > 0 {
			return Answer{}, fmt.Errorf("takes text only")
		}
		return Answer{Text: text}, nil
	}

	seen := map[string]bool{}
	var choices []string
	for _, c := range a.Choices {
		if !q.hasOption(c) {
			return Answer{}, fmt.Errorf("unknown option %q", c)
		}
		if !seen[c] {
			seen[c] = true
			choices = append(choices, c)
		}
	}

	switch q.Kind {
	case "single":
		if len(choices) > 1 {
			return Answer{}, fmt.Errorf("takes one option")
		}
	case "multi":
		if q.Max > 0 && len(choices) > q.Max {
			return Answer{}, fmt.Errorf("takes at most %d options", q.Max)
		}
		if q.Exclusive != "" && seen[q.Exclusive] && len(choices) > 1 {
			return Answer{}, fmt.Errorf("%q can't be combined with other options", q.Exclusive)
		}
	default:
		return Answer{}, fmt.Errorf("unsupported question kind %q", q.Kind)
	}

	switch {
	case q.TextAlways:
	case q.TextOption != "" && seen[q.TextOption]:
	default:
		text = ""
	}
	return Answer{Choices: choices, Text: text}, nil
}
