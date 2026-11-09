package ai

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/investigation"
)

func TestTurnPromptForQuestionRuns(t *testing.T) {
	opening := investigation.Question{Text: "checkout is slow", Namespaces: []string{"shop"}, LookBack: "1h"}
	targetless := Request{Opening: opening}
	targeted := Request{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "checkout", Opening: opening}

	if p := turnPrompt(targetless); !strings.Contains(p, "Start with Radar's `issues`") {
		t.Fatalf("a question with no resource opens with the question prompt:\n%s", p)
	}
	if p := turnPrompt(targeted); !strings.Contains(p, "They asked about Deployment.apps shop/checkout") {
		t.Fatalf("a question about a resource opens with the targeted question prompt:\n%s", p)
	}
	followUp := targetless
	followUp.Question = "what about payments?"
	if p := turnPrompt(followUp); !strings.HasPrefix(p, "what about payments?") {
		t.Fatalf("a follow-up answers its own question, not the opening one again:\n%s", p)
	}
	apply := targeted
	apply.Apply, apply.Fix = true, "set replicas to 2"
	if p := turnPrompt(apply); !strings.HasPrefix(p, "Apply EXACTLY this fix") {
		t.Fatalf("an apply turn stays bound to the confirmed fix:\n%s", p)
	}
}

func TestSystemPromptForQuestionRuns(t *testing.T) {
	opening := investigation.Question{Text: "checkout is slow"}
	if got := systemPrompt(Request{Opening: opening}); got != investigation.QuestionSystemPrompt {
		t.Fatal("a question run's read-only session gets the question framing")
	}
	if got := systemPrompt(Request{Opening: opening, Apply: true, Name: "checkout"}); got != investigation.SystemPrompt {
		t.Fatal("an apply session acts on one resource and keeps the resource framing")
	}
	if got := systemPrompt(Request{Name: "checkout"}); got != investigation.SystemPrompt {
		t.Fatal("a resource run is unchanged")
	}
}

func TestQuestionRunsAreNeverFocusedOntoALiveRun(t *testing.T) {
	m, live, _ := controlledRunManager(t, nil)
	live.status = "running"
	spec := StartSpec{
		Kind: live.Kind, Namespace: live.Namespace, Name: live.Name,
		Agent: live.Agent, Profile: live.Profile, Question: "why did it restart?",
	}
	got, err := m.Start(spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got.ID == live.ID {
		t.Fatal("a question about a resource with a live run must start its own investigation")
	}
	if got.Question != "why did it restart?" {
		t.Fatalf("summary question = %q", got.Question)
	}
}

func TestApplyNeedsAResource(t *testing.T) {
	m, r, _ := controlledRunManager(t, nil)
	r.Kind, r.Namespace, r.Name, r.Question = "", "", "", "checkout is slow"
	if err := m.AddTurn(r.ID, "", true, "set replicas to 2", false); !errors.Is(err, ErrApplyNeedsTarget) {
		t.Fatalf("AddTurn(apply) on a question-only run = %v, want ErrApplyNeedsTarget", err)
	}
}

func TestQuestionSurvivesRestart(t *testing.T) {
	asked := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	dbPath := filepath.Join(t.TempDir(), "ai-runs.db")
	st, err := OpenRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st.SaveRun(RunSummary{
		ID: "run-q", Context: "ctx-a", Agent: "claude", Profile: ExecutionProfileSafeguarded,
		Question: "checkout is slow", Namespaces: []string{"shop", "payments"}, LookBack: "6h",
		AskedAt: &asked, Status: "done", SessionID: "s", CreatedAt: nowUTC(), UpdatedAt: nowUTC(),
	})
	st.Close()

	st2, err := OpenRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	m := NewRunManager(nil, func() string { return "" }, "", func() string { return "ctx-a" }, st2)
	t.Cleanup(m.Shutdown)
	r := m.Get("run-q")
	if r == nil {
		t.Fatal("run not reloaded")
	}
	got := r.Summary()
	if got.Question != "checkout is slow" || !slices.Equal(got.Namespaces, []string{"shop", "payments"}) || got.LookBack != "6h" ||
		got.AskedAt == nil || !got.AskedAt.Equal(asked) || !r.askedAt().Equal(asked) {
		t.Fatalf("reloaded summary lost the opening question: %+v", got)
	}
}

// A plain resource start must not be focused onto a question someone asked
// about the same resource: it would get their question-led answer instead
// of a health assessment.
func TestResourceStartIsNotFocusedOntoAQuestionRun(t *testing.T) {
	m, live, _ := controlledRunManager(t, nil)
	live.status = "running"
	live.Question = "why did it restart?"
	got, err := m.Start(StartSpec{
		Kind: live.Kind, Namespace: live.Namespace, Name: live.Name,
		Agent: live.Agent, Profile: live.Profile,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got.ID == live.ID {
		t.Fatal("a resource-only start focused onto a live question run")
	}
}
