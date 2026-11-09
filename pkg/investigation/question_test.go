package investigation

import (
	"strings"
	"testing"
	"time"
)

var askedAt = time.Date(2026, 10, 28, 14, 5, 0, 0, time.UTC)

func TestQuestionTaskPromptStartsWhereTheEngineerSaid(t *testing.T) {
	q := Question{Text: "checkout is slow\nsince lunch", Namespaces: []string{"shop", "payments"}, LookBack: "1h", AskedAt: askedAt}
	p := QuestionTaskPrompt(q, Options{})
	for _, want := range []string{
		"> checkout is slow\n> since lunch\n",
		"They asked at 2026-10-28T14:05:00Z and said it started within the hour before that.",
		"`get_changes` (since=1h) in namespaces shop, payments",
		"`search` for any resource names",
		"VERDICT BLOCK:", "PLACING A CARD:",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "say so briefly and stop") {
		t.Fatalf("a question run must not inherit the resource health opening:\n%s", p)
	}
	if p := QuestionTaskPrompt(Question{Text: "why?", AskedAt: askedAt}, Options{}); !strings.Contains(p, "`get_changes` across the cluster") {
		t.Fatalf("no namespaces means the whole cluster:\n%s", p)
	}
}

// A healthy resource must not end an investigation of something that
// happened earlier, and a current unrelated issue must not replace the
// question: the health frame is context.
func TestTargetQuestionPromptLeadsWithTheQuestion(t *testing.T) {
	target := Target{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "checkout"}
	health := &HealthSignal{Health: "healthy"}
	p := TargetQuestionPrompt(target, health, Question{Text: "why did checkout fail yesterday?", AskedAt: askedAt}, Options{})
	if !strings.HasPrefix(p, "The engineer describes what they are seeing") {
		t.Fatalf("the question must lead:\n%s", p)
	}
	for _, want := range []string{
		"They asked about Deployment.apps shop/checkout",
		"as context only: Radar currently reports Deployment.apps shop/checkout as healthy",
		"Pass `group=apps`",
		"VERDICT BLOCK:",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "say so briefly and stop") || strings.Contains(p, "Find the specific root cause and propose") {
		t.Fatalf("the health-driven opening must not steer a question:\n%s", p)
	}
}

func TestQuestionSystemPromptKeepsTheSharedDiscipline(t *testing.T) {
	if !strings.Contains(QuestionSystemPrompt, ResultDiscipline+" AUDIENCE:") {
		t.Fatal("QuestionSystemPrompt must carry ResultDiscipline verbatim")
	}
	if !strings.HasSuffix(QuestionSystemPrompt, " "+UntrustedClusterData) {
		t.Fatal("QuestionSystemPrompt must end with UntrustedClusterData verbatim")
	}
	if !strings.Contains(QuestionSystemPrompt, "never guess a resource") {
		t.Fatal("a vague question must narrow or end inconclusive")
	}
}

func TestLookBackLabel(t *testing.T) {
	for in, want := range map[string]string{"15m": "15 minutes", "1h": "hour", "6h": "6 hours", "24h": "24 hours", "": "", "1d": "", "h": "", "0h": ""} {
		if got := lookBackLabel(in); got != want {
			t.Errorf("lookBackLabel(%q) = %q, want %q", in, got, want)
		}
	}
	for _, lb := range LookBacks {
		if lookBackLabel(lb) == "" {
			t.Errorf("offered look-back %q has no label", lb)
		}
	}
}

// A re-asked question opens later than it was asked; its change window
// still has to cover the incident the engineer described.
func TestQuestionTaskPromptWidensTheChangeWindowForALaterRun(t *testing.T) {
	q := Question{Text: "checkout 502s", LookBack: "1h", AskedAt: askedAt, OpensAt: askedAt.Add(5 * time.Hour)}
	if p := QuestionTaskPrompt(q, Options{}); !strings.Contains(p, "`get_changes` (since=6h)") {
		t.Fatalf("window must reach back to the incident:\n%s", p)
	}
	q.OpensAt = askedAt.Add(3 * 24 * time.Hour)
	if p := QuestionTaskPrompt(q, Options{}); !strings.Contains(p, "(since=4d)") {
		t.Fatalf("a days-old question needs a days-long window:\n%s", p)
	}
	q.OpensAt = time.Time{}
	q.LookBack = "15m"
	if p := QuestionTaskPrompt(q, Options{}); !strings.Contains(p, "(since=15m)") {
		t.Fatalf("a fresh question keeps its look-back:\n%s", p)
	}
}

// The opening turn starts moments after the question is asked; that delay
// must not round the engineer's look-back up to the next hour.
func TestQuestionTaskPromptIgnoresTheOpeningDelay(t *testing.T) {
	for lookBack, want := range map[string]string{"15m": "since=15m", "1h": "since=1h", "6h": "since=6h", "24h": "since=24h"} {
		q := Question{Text: "checkout 502s", LookBack: lookBack, AskedAt: askedAt, OpensAt: askedAt.Add(800 * time.Millisecond)}
		if p := QuestionTaskPrompt(q, Options{}); !strings.Contains(p, "("+want+")") {
			t.Errorf("look-back %s: want %s in:\n%s", lookBack, want, p)
		}
	}
	q := Question{Text: "checkout 502s", LookBack: "15m", AskedAt: askedAt, OpensAt: askedAt.Add(4*time.Minute + time.Second)}
	if p := QuestionTaskPrompt(q, Options{}); !strings.Contains(p, "(since=20m)") {
		t.Fatalf("a run four minutes later widens by whole minutes:\n%s", p)
	}
}

func TestTargetQuestionPromptCarriesTheWindowAndNamespaces(t *testing.T) {
	target := Target{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "checkout"}
	q := Question{Text: "slow since yesterday", Namespaces: []string{"payments"}, LookBack: "24h", AskedAt: askedAt}
	p := TargetQuestionPrompt(target, nil, q, Options{})
	for _, want := range []string{"`get_changes` (since=24h)", "They also want it checked in namespace payments."} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
}
