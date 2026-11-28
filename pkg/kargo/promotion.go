package kargo

import (
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Promotion phases. Failed is a business failure, Errored a technical one;
// both leave the Stage without the Freight. Aborted is a person cancelling.
const (
	PhasePending   = "Pending"
	PhaseRunning   = "Running"
	PhaseSucceeded = "Succeeded"
	PhaseFailed    = "Failed"
	PhaseErrored   = "Errored"
	PhaseAborted   = "Aborted"
)

// PromotionFailed reports a Promotion phase that is a failure. Aborted is a
// cancellation, not a failure.
func PromotionFailed(phase string) bool {
	return phase == PhaseFailed || phase == PhaseErrored
}

// PromotionTerminal reports a phase the Promotion won't leave.
func PromotionTerminal(phase string) bool {
	switch phase {
	case PhaseSucceeded, PhaseFailed, PhaseErrored, PhaseAborted:
		return true
	}
	return false
}

// StepResult is how one promotion step ran, from
// status.stepExecutionMetadata, which lists the steps in spec order.
type StepResult struct {
	Alias string
	// Status is Running, Succeeded, Failed, Errored, Aborted or Skipped;
	// empty for a step that hasn't started.
	Status          string
	Message         string
	ErrorCount      int64
	ContinueOnError bool
	StartedAt       time.Time
	FinishedAt      time.Time
}

func (s StepResult) failed() bool {
	return s.Status == PhaseFailed || s.Status == PhaseErrored
}

// Step is one promotion step: what it runs and how it ran.
type Step struct {
	// Uses is the step kind, such as git-clone or argocd-update; empty for a
	// step that runs a PromotionTask, named by Task.
	Uses string
	Task string
	As   string
	StepResult
}

// Promotion is a Promotion object's target, phase and steps.
type Promotion struct {
	Namespace string
	Name      string
	Stage     string
	// FreightName is the Freight being promoted; Freight is the Promotion's
	// record of it once it has started.
	FreightName string
	Freight     *FreightRef
	Phase       string
	Message     string
	StartedAt   time.Time
	FinishedAt  time.Time
	Steps       []Step
}

// ParsePromotion reads a Promotion object.
func ParsePromotion(u *unstructured.Unstructured) Promotion {
	if u == nil {
		return Promotion{}
	}
	p := Promotion{
		Namespace:   u.GetNamespace(),
		Name:        u.GetName(),
		Stage:       str(u.Object, "spec", "stage"),
		FreightName: str(u.Object, "spec", "freight"),
		Phase:       str(u.Object, "status", "phase"),
		Message:     str(u.Object, "status", "message"),
		StartedAt:   timeAt(u.Object, "status", "startedAt"),
		FinishedAt:  timeAt(u.Object, "status", "finishedAt"),
	}
	if f := object(u.Object, "status", "freight"); f != nil {
		ref := parseFreightRef(f)
		p.Freight = &ref
	}
	results := stepResults(object(u.Object, "status"))
	for i, s := range objects(u.Object, "spec", "steps") {
		step := Step{Uses: str(s, "uses"), Task: str(s, "task", "name"), As: str(s, "as")}
		if i < len(results) {
			step.StepResult = results[i]
		}
		if step.Alias == "" {
			step.Alias = step.As
		}
		p.Steps = append(p.Steps, step)
	}
	return p
}

// Failure is the reason the Promotion failed, chosen as Failure chooses it.
func (p Promotion) Failure() Failure {
	results := make([]StepResult, len(p.Steps))
	for i, s := range p.Steps {
		results[i] = s.StepResult
	}
	return failureOf(p.Phase, p.Message, results)
}

// Failure is why a Promotion failed.
type Failure struct {
	// Failed: the Promotion's phase is Failed or Errored.
	Failed bool
	// Step is the index of the step that failed it, -1 when the message
	// comes from the Promotion itself or is unknown.
	Step      int
	StepAlias string
	// Message is the failing step's message, else the Promotion's own, else
	// empty: unknown.
	Message string
}

// failureOf picks the first step that failed without continueOnError, whose
// message is the most specific, else the Promotion's status message.
func failureOf(phase, message string, steps []StepResult) Failure {
	if !PromotionFailed(phase) {
		return Failure{Step: -1}
	}
	for i, s := range steps {
		if s.failed() && !s.ContinueOnError {
			if s.Message != "" {
				return Failure{Failed: true, Step: i, StepAlias: s.Alias, Message: s.Message}
			}
			return Failure{Failed: true, Step: i, StepAlias: s.Alias, Message: message}
		}
	}
	return Failure{Failed: true, Step: -1, Message: message}
}

// Cause is a promotion failure message without the wrappers Kargo's
// promotion engine puts around a step's own error, so the step's error leads.
// Kargo v1.12 writes them in pkg/promotion: `an unrecoverable error occurred:
// `, `step "<alias>" met error threshold of <n>: `, `step "<alias>" errored: `
// (local_orchestrator.go), `error running step "<alias>": ` and
// `step "<alias>": ` (local_executor.go). Wrappers that carry the cause
// themselves, such as a timeout, stay; text in any other form is returned
// unchanged.
func Cause(message string) string {
	out := strings.TrimSpace(message)
	for {
		rest, ok := stripKargoWrapper(out)
		if !ok || strings.TrimSpace(rest) == "" {
			return out
		}
		out = strings.TrimSpace(rest)
	}
}

func stripKargoWrapper(s string) (string, bool) {
	if rest, ok := strings.CutPrefix(s, "an unrecoverable error occurred: "); ok {
		return rest, true
	}
	if rest, ok := strings.CutPrefix(s, "error running step "); ok {
		return afterAlias(rest, ": ")
	}
	rest, ok := strings.CutPrefix(s, "step ")
	if !ok {
		return "", false
	}
	quoted, err := strconv.QuotedPrefix(rest)
	if err != nil {
		return "", false
	}
	rest = rest[len(quoted):]
	if after, ok := strings.CutPrefix(rest, ": "); ok {
		return after, true
	}
	if after, ok := strings.CutPrefix(rest, " errored: "); ok {
		return after, true
	}
	if after, ok := strings.CutPrefix(rest, " met error threshold of "); ok {
		digits := strings.IndexFunc(after, func(r rune) bool { return r < '0' || r > '9' })
		if digits > 0 {
			return strings.CutPrefix(after[digits:], ": ")
		}
	}
	return "", false
}

// afterAlias cuts a Go-quoted step alias and the separator after it.
func afterAlias(s, sep string) (string, bool) {
	quoted, err := strconv.QuotedPrefix(s)
	if err != nil {
		return "", false
	}
	return strings.CutPrefix(s[len(quoted):], sep)
}

func stepResults(status map[string]any) []StepResult {
	var out []StepResult
	for _, m := range objects(status, "stepExecutionMetadata") {
		count := integer(m, "errorCount")
		out = append(out, StepResult{
			Alias:           str(m, "alias"),
			Status:          str(m, "status"),
			Message:         str(m, "message"),
			ErrorCount:      count,
			ContinueOnError: boolean(m, "continueOnError"),
			StartedAt:       timeAt(m, "startedAt"),
			FinishedAt:      timeAt(m, "finishedAt"),
		})
	}
	return out
}
