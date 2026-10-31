package investigation

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaxQuestionLength bounds the engineer's opening question. It is trusted
// input, but it travels in every opening prompt and in the run history.
const MaxQuestionLength = 2000

// LookBacks are the windows an engineer can say a problem started within,
// in the order a picker offers them.
var LookBacks = []string{"15m", "1h", "6h", "24h"}

// Question is the engineer's own description of what they are seeing, and
// where and since when to start looking. It opens a run instead of, or in
// addition to, a named resource.
type Question struct {
	Text string
	// Namespaces are where to start looking, not a boundary: the cause of a
	// symptom in one namespace often lives in another, or on a node. Empty
	// means the whole cluster.
	Namespaces []string
	// LookBack is one of LookBacks, relative to AskedAt.
	LookBack string
	AskedAt  time.Time
	// OpensAt is when this run opens; zero means AskedAt. A run that re-asks
	// an earlier question opens later, and its change window has to reach
	// back to the incident the engineer described, not just the look-back.
	OpensAt time.Time
}

// QuestionSystemPrompt frames a run opened by the engineer's question. It
// replaces SystemPrompt on such a run's fresh read-only sessions; apply turns
// keep SystemPrompt, because an apply acts on one confirmed resource.
const QuestionSystemPrompt = "You are a senior Kubernetes SRE answering an engineer who has described a problem they are seeing in their cluster. They will act on your answer and may not know Kubernetes well. " +
	"Radar records every tool result you read, and it will show your final answer as a Findings page: your headline, what you could not settle, your story with Radar's own evidence cards placed where you cite them, and the next steps. Radar renders the cards; you decide where they belong. " +
	"METHOD: the engineer's description is the question; Radar's signals are evidence, not the agenda. An active issue that does not bear on what they described is context at most. Find the resources the description is about before deciding anything: read Radar's issues and recent changes where they said to start, search for the names they used, then go deep with targeted calls where a live hypothesis needs it, and say in a few words what you are checking before each call. Follow the evidence wherever it leads, and say so when it leaves the place they said to start. Do not ask permission to look. If the description is too vague to point at anything after a first look, ask one short question that would narrow it, or answer inconclusive; never guess a resource. Where these instructions say the resource or the target, read it as the resources your answer is about, and name them in each cited item's subject. " +
	ResultDiscipline + " " +
	"AUDIENCE: plain words first, the exact field, image, config or command second; gloss a Kubernetes term once when you must use it. " +
	UntrustedClusterData

// QuestionTaskPrompt opens a run that has only the engineer's question.
func QuestionTaskPrompt(q Question, opts Options) string {
	prompt := questionOpening(q) +
		" Start with Radar's `issues` and `get_changes`" + changesWindow(q) + " " + whereToStart(q) + ", and `search` for any resource names in the description." +
		" If the description is a lookup rather than a problem, answer it briefly in the summary and story, with healthy=true only for what you verified." +
		" " + answerFormatInstruction + verdictContract(opts.Citations) + storyGuidance
	return withMetrics(prompt, opts.Metrics)
}

// TargetQuestionPrompt opens a run about one resource that the engineer
// started with a question. Radar's health frame is context for the question;
// it never replaces it, so a currently healthy resource does not end an
// investigation of something that happened an hour ago.
func TargetQuestionPrompt(target Target, health *HealthSignal, q Question, opts Options) string {
	toolGuidance := ""
	if target.Group != "" {
		toolGuidance = fmt.Sprintf(" Pass `group=%s` to every Radar tool that accepts an API group so same-kind resources cannot be confused.", target.Group)
	}
	prompt := questionOpening(q) +
		" They asked about " + target.Label() + "; start there, and follow the evidence to adjacent objects when it points elsewhere." +
		alsoLookIn(q) + targetChanges(q) +
		" What Radar currently records for it, as context only: " + healthFrame(target.Label(), health) +
		toolGuidance + " " + answerFormatInstruction + verdictContract(opts.Citations) + storyGuidance
	return withMetrics(prompt, opts.Metrics)
}

func alsoLookIn(q Question) string {
	if len(q.Namespaces) == 0 {
		return ""
	}
	return " They also want it checked " + whereToStart(q) + "."
}

// targetChanges asks for the resource's changes over the engineer's window;
// a bare get_changes covers only the last hour.
func targetChanges(q Question) string {
	window := changesWindow(q)
	if window == "" {
		return ""
	}
	return " Read what changed around it with `get_changes`" + window + "."
}

func questionOpening(q Question) string {
	var b strings.Builder
	b.WriteString("The engineer describes what they are seeing, in their own words:\n\n")
	for line := range strings.SplitSeq(strings.TrimSpace(q.Text), "\n") {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	if !q.AskedAt.IsZero() {
		fmt.Fprintf(&b, "They asked at %s", q.AskedAt.UTC().Format(time.RFC3339))
		if label := lookBackLabel(q.LookBack); label != "" {
			fmt.Fprintf(&b, " and said it started within the %s before that", label)
		}
		b.WriteString(". Every read you make is live, taken after they asked; say so when the time matters.")
	}
	return b.String()
}

func changesWindow(q Question) string {
	lookBack, err := time.ParseDuration(q.LookBack)
	if q.LookBack == "" || err != nil {
		return ""
	}
	// A turn opens moments after its question is asked; only a gap of a
	// minute or more (a re-run, a narrowing run) widens the window, rounded
	// up to the minute so the engineer's own choice reads back unchanged.
	if !q.AskedAt.IsZero() {
		if gap := q.OpensAt.Sub(q.AskedAt); gap >= time.Minute {
			lookBack += (gap + time.Minute - 1).Truncate(time.Minute)
		}
	}
	return " (since=" + sinceDuration(lookBack) + ")"
}

// sinceDuration spells d the way get_changes reads it, rounded up so the
// window never starts after the incident did: 15m, 7h, 3d.
func sinceDuration(d time.Duration) string {
	switch {
	case d < time.Hour && d%time.Minute == 0:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d <= 48*time.Hour:
		return strconv.Itoa(int((d+time.Hour-1)/time.Hour)) + "h"
	default:
		return strconv.Itoa(int((d+24*time.Hour-1)/(24*time.Hour))) + "d"
	}
}

func whereToStart(q Question) string {
	switch len(q.Namespaces) {
	case 0:
		return "across the cluster"
	case 1:
		return "in namespace " + q.Namespaces[0]
	default:
		return "in namespaces " + strings.Join(q.Namespaces, ", ")
	}
}

func lookBackLabel(lookBack string) string {
	if lookBack == "" {
		return ""
	}
	unit := lookBack[len(lookBack)-1:]
	n, err := strconv.Atoi(lookBack[:len(lookBack)-1])
	if err != nil || n <= 0 {
		return ""
	}
	words := map[string]string{"m": "minute", "h": "hour"}
	word, ok := words[unit]
	if !ok {
		return ""
	}
	if n == 1 {
		return word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
