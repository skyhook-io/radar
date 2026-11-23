package server

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	substrateapi "github.com/skyhook-io/radar/internal/substrate"
	"github.com/skyhook-io/radar/internal/substrate/ateapi"
	model "github.com/skyhook-io/radar/pkg/substrate"
)

// Names and revisions read from a live kagent 1.0 cluster.
const (
	helperRevision  = "aa5e9426eb2ead1c0f65f5c97cf535a9f41038ecd7f8f2b9a7ecc6d925987f40"
	helperTemplate  = "k8s-helper-aa5e9426eb2e"
	helperSession   = "session-01a1faa2-b3e3-7dd3-ae2d-3114122d4291"
	crashedSession  = "session-7c9e6679-7425-40de-944b-e07fc1f90ae7"
	sandboxSession  = "session-9b2d6c1e-4f3a-4e8b-9c7d-1a2b3c4d5e6f"
	earlierSession  = "session-2c4e6a8b-0d1f-4a3b-8c5d-7e9f0a1b2c3d"
	kagentSandboxed = "sandbox-3f2c1b0a9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b"
)

var kagentHelper = model.KagentAgent{Namespace: "kagent", Name: "k8s-helper", UID: "47548047-6e64-491a-8475-7c7522620286",
	DesiredRevision: helperRevision, LatestSuccessfulRevision: helperRevision}

// kagentFake is defaultFake plus the kagent atespace: two sessions of
// k8s-helper (one crashed), a sandbox's session on the shared worker, and a
// session on an earlier revision of k8s-helper.
func kagentFake() *fakeControl {
	f := defaultFake()
	now := time.Now()
	f.atespaces = append(f.atespaces, "kagent")
	crashed := fakeActor("kagent", crashedSession, helperTemplate, ateapi.ActorState_ACTOR_STATE_CRASHED, now.Add(-2*time.Minute))
	crashed.Status.Crash = &ateapi.ActorCrash{Message: "exit 1", CrashTime: timestamppb.New(now.Add(-2 * time.Minute))}
	f.actors = append(f.actors,
		fakeActor("kagent", helperSession, helperTemplate, ateapi.ActorState_ACTOR_STATE_SUSPENDED, now.Add(-time.Hour)),
		crashed,
		assign(fakeActor("kagent", sandboxSession, kagentSandboxed, ateapi.ActorState_ACTOR_STATE_RUNNING, now.Add(-3*time.Minute)), "w-1", "agent-pool", "agent-pool-abc"),
		fakeActor("kagent", earlierSession, "k8s-helper-0123456789ab", ateapi.ActorState_ACTOR_STATE_SUSPENDED, now.Add(-2*time.Hour)),
	)
	f.templates = append(f.templates,
		&ateapi.ActorTemplate{Metadata: &ateapi.ResourceMetadata{Atespace: "kagent", Name: helperTemplate},
			Status: &ateapi.ActorTemplateStatus{GoldenSnapshotStatus: &ateapi.GoldenSnapshotStatus{ErrorMessage: "GoldenActorCrashed: exit 1"}}},
		&ateapi.ActorTemplate{Metadata: &ateapi.ResourceMetadata{Atespace: "kagent", Name: kagentSandboxed}},
	)
	return f
}

// kagentServer is a local Radar on a cluster serving kagent, whose callers
// read Agents as readers says: by username, the namespaces they may list
// Agents in. Every read is recorded in asked.
func kagentServer(t *testing.T, readers map[string][]string) (*Server, *[]string) {
	t.Helper()
	s := substrateTestServer(t, substrateapi.Kube{KagentServed: true})
	connectFake(t, s, kagentFake(), fullToken)
	prevServed, prevAgents := substrateKagentServed, substrateKagentAgents
	t.Cleanup(func() { substrateKagentServed, substrateKagentAgents = prevServed, prevAgents })
	substrateKagentServed = func() bool { return true }
	var asked []string
	substrateKagentAgents = func(_ *Server, r *http.Request, requested []string) substrateKagentRead {
		user := ""
		if u := auth.UserFromContext(r.Context()); u != nil {
			user = u.Username
		}
		asked = append(asked, user)
		namespaces := readers[user]
		if len(namespaces) == 0 {
			return substrateKagentRead{Served: true, Access: integration.KindAccess{State: integration.KindCoverageDenied}}
		}
		access := integration.AccessFromScope(namespaces, true)
		// As readWorkspaceKind names the denied namespaces the caller asked for.
		for _, ns := range requested {
			if !slices.Contains(namespaces, ns) {
				access.Denied = append(access.Denied, ns)
			}
		}
		var agents []model.KagentAgent
		for _, ns := range namespaces {
			if ns == "kagent" {
				agents = append(agents, kagentHelper)
			}
		}
		return substrateKagentRead{Served: true, Agents: agents, Access: access}
	}
	return s, &asked
}

var (
	admin  = &auth.User{Username: "admin"}
	viewer = &auth.User{Username: "viewer"}
)

func rowNamed(rows []model.ActorRow, name string) *model.ActorRow {
	for i := range rows {
		if rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}

// Attribution is the caller's: a caller who can't read Agents, asking after
// one who can warmed the shared read, sees no Agent named.
func TestSubstrateKagentAttributionIsPerCaller(t *testing.T) {
	s, asked := kagentServer(t, map[string][]string{"admin": {"kagent"}})

	var actors substrateActorsResponse
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?atespace=kagent", user: admin}, &actors)
	helper := rowNamed(actors.Rows, helperSession)
	if helper == nil || helper.Kagent == nil || helper.Kagent.Kind != model.KagentAgentSession || helper.Kagent.Agent == nil ||
		*helper.Kagent.Agent != kagentHelper.Ref() || !helper.Kagent.NewSessions || helper.Kagent.SessionID != "01a1faa2-b3e3-7dd3-ae2d-3114122d4291" {
		t.Fatalf("k8s-helper's session for admin = %+v", helper)
	}
	if r := rowNamed(actors.Rows, sandboxSession); r == nil || r.Kagent == nil || r.Kagent.Kind != model.KagentSandbox {
		t.Errorf("the sandbox's session = %+v", r)
	}
	if r := rowNamed(actors.Rows, earlierSession); r == nil || r.Kagent.Reason != model.KagentEarlierRevision || len(r.Kagent.Candidates) != 1 {
		t.Errorf("the earlier revision's session = %+v", r)
	}
	if actors.KagentAgents == nil || actors.KagentAgents.State != integration.KindCoveragePartial {
		t.Errorf("agents coverage = %+v", actors.KagentAgents)
	}
	var research substrateActorsResponse
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?atespace=research", user: admin}, &research)
	if r := rowNamed(research.Rows, "lit-0201"); r == nil || r.Kagent != nil {
		t.Errorf("a non-kagent actor = %+v", r)
	}

	for _, c := range []struct {
		h      http.HandlerFunc
		path   string
		params map[string]string
	}{
		{s.handleSubstrateActors, "/api/substrate/actors", nil},
		{s.handleSubstrateActor, "/a", map[string]string{"atespace": "kagent", "name": helperSession}},
		{s.handleSubstrateWorkers, "/api/substrate/workers", nil},
		{s.handleSubstrateTemplates, "/api/substrate/templates", nil},
		{s.handleSubstrateWorkspace, "/api/substrate/workspace", nil},
	} {
		rec := call(t, s, c.h, substrateCall{path: c.path, params: c.params, user: viewer}, nil)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || strings.Contains(body, `"agent":{`) || strings.Contains(body, `"candidates"`) || strings.Contains(body, "Agent kagent/k8s-helper") {
			t.Errorf("%s for a caller who can't read Agents = %d %s", c.path, rec.Code, body)
		}
	}
	var mine substrateActorsResponse
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?atespace=kagent", user: viewer}, &mine)
	if r := rowNamed(mine.Rows, helperSession); r == nil || r.Kagent == nil || r.Kagent.Kind != model.KagentUnattributed || r.Kagent.Reason != model.KagentAgentNotReadable {
		t.Errorf("k8s-helper's session for viewer = %+v", r)
	}
	if r := rowNamed(mine.Rows, sandboxSession); r == nil || r.Kagent.Kind != model.KagentSandbox {
		t.Errorf("a sandbox needs no Agent: %+v", r)
	}
	if mine.KagentAgents == nil || mine.KagentAgents.State != integration.KindCoverageDenied {
		t.Errorf("viewer's agents coverage = %+v", mine.KagentAgents)
	}
	if !strings.Contains(strings.Join(*asked, ","), "viewer") {
		t.Errorf("Agents were not read as the viewer: %v", *asked)
	}
}

// The agent and kagent filters apply before counts and paging.
func TestSubstrateKagentFiltersBeforePaging(t *testing.T) {
	s, _ := kagentServer(t, map[string][]string{"": {"kagent"}})
	list := func(path string) (out substrateActorsResponse) {
		call(t, s, s.handleSubstrateActors, substrateCall{path: path}, &out)
		return out
	}
	actors := list("/api/substrate/actors?agent=kagent/k8s-helper&limit=1")
	if actors.Total == nil || *actors.Total != 2 || len(actors.Rows) != 1 || actors.Counts.Total != 2 || actors.Counts.ByState[model.StateCrashed] != 1 {
		t.Fatalf("agent filter = total %v rows %d counts %+v", actors.Total, len(actors.Rows), actors.Counts)
	}
	actors = list("/api/substrate/actors?agent=kagent/k8s-helper&state=crashed")
	if *actors.Total != 1 || actors.Rows[0].Name != crashedSession || actors.Counts.Total != 2 {
		t.Errorf("agent and state = %+v", actors.Rows)
	}
	for q, want := range map[string]string{"unattributed": earlierSession, "sandbox": sandboxSession} {
		actors = list("/api/substrate/actors?kagent=" + q)
		if actors.Total == nil || *actors.Total != 1 || actors.Rows[0].Name != want {
			t.Errorf("kagent=%s = %+v", q, actors.Rows)
		}
	}
	actors = list("/api/substrate/actors?agent=kagent/spike-agent")
	if actors.Total == nil || *actors.Total != 0 {
		t.Errorf("an Agent with no sessions = %v", actors.Total)
	}
	for _, q := range []string{"agent=k8s-helper", "agent=kagent/", "agent=/k8s-helper", "kagent=probable"} {
		if rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?" + q}, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d", q, rec.Code)
		}
	}
}

// The actor page, the workers' assigned actors and the template rows carry
// the attribution, and steps name the Agent; a failed golden snapshot of an
// Agent's template is never "recreate the template".
func TestSubstrateKagentDetailWorkersTemplates(t *testing.T) {
	s, _ := kagentServer(t, map[string][]string{"": {"kagent"}})
	var detail substrateActorDetail
	call(t, s, s.handleSubstrateActor, substrateCall{path: "/a", params: map[string]string{"atespace": "kagent", "name": crashedSession}}, &detail)
	if detail.Actor.Kagent == nil || detail.Row.Kagent == nil || detail.Row.Kagent.Agent == nil || detail.Coverage.KagentAgents == nil {
		t.Fatalf("detail = actor %+v row %+v coverage %+v", detail.Actor.Kagent, detail.Row.Kagent, detail.Coverage.KagentAgents)
	}
	if detail.Template == nil || detail.Template.Kagent == nil || detail.Template.Kagent.Agent == nil {
		t.Errorf("detail template = %+v", detail.Template)
	}
	var crash *model.Attention
	for i := range detail.Attention {
		if detail.Attention[i].Rule == model.RuleCrashedActors {
			crash = &detail.Attention[i]
		}
	}
	if crash == nil || !strings.HasPrefix(crash.NextStep, "This actor is a session of kagent Agent kagent/k8s-helper.") || crash.Command != "" {
		t.Errorf("the crash step = %+v", crash)
	}

	var workers substrateWorkersResponse
	call(t, s, s.handleSubstrateWorkers, substrateCall{path: "/api/substrate/workers"}, &workers)
	found := false
	for _, a := range workers.Workers[0].Actors {
		if a.Name == sandboxSession {
			found = a.Kagent != nil && a.Kagent.Kind == model.KagentSandbox
		} else if a.Kagent != nil {
			t.Errorf("a non-kagent assigned actor = %+v", a)
		}
	}
	if !found || workers.KagentAgents == nil {
		t.Errorf("assigned actors = %+v", workers.Workers[0].Actors)
	}

	var templates substrateTemplatesResponse
	call(t, s, s.handleSubstrateTemplates, substrateCall{path: "/api/substrate/templates"}, &templates)
	for _, row := range templates.Templates {
		switch row.Name {
		case helperTemplate:
			if row.Kagent == nil || row.Kagent.Agent == nil || *row.Kagent.Agent != kagentHelper.Ref() {
				t.Errorf("k8s-helper's template = %+v", row.Kagent)
			}
		case kagentSandboxed:
			if row.Kagent == nil || row.Kagent.Kind != model.KagentSandbox {
				t.Errorf("the sandbox template = %+v", row.Kagent)
			}
		default:
			if row.Kagent != nil {
				t.Errorf("%s = %+v", row.Name, row.Kagent)
			}
		}
	}

	var ws substrateWorkspaceResponse
	call(t, s, s.handleSubstrateWorkspace, substrateCall{path: "/api/substrate/workspace"}, &ws)
	for _, r := range ws.NeedsAttention {
		if r.Rule != model.RuleGoldenFailed {
			continue
		}
		recreate := strings.Contains(r.NextStep, "recreate the template")
		if r.Subject.Name == helperTemplate && (recreate || !strings.Contains(r.NextStep, "kagent/k8s-helper")) || r.Subject.Name != helperTemplate && !recreate {
			t.Errorf("golden row %s = %q", r.Subject.Name, r.NextStep)
		}
	}
	if ws.Coverage.KagentAgents == nil {
		t.Errorf("workspace agents coverage absent")
	}
}

// The Agent's sessions: exact counts from a complete read; lower bounds when
// the actors read stops at its budget; refusals for an Agent the caller
// can't read or that doesn't exist.
func TestSubstrateKagentAgentSessions(t *testing.T) {
	s, _ := kagentServer(t, map[string][]string{"": {"kagent"}, "viewer": {"research"}})
	sessions := func(user *auth.User, name string, out any) int {
		return call(t, s, s.handleSubstrateKagentAgentSessions, substrateCall{path: "/s", user: user, params: map[string]string{"namespace": "kagent", "name": name}}, out).Code
	}
	var resp substrateKagentSessionsResponse
	if code := sessions(nil, "k8s-helper", &resp); code != http.StatusOK {
		t.Fatalf("sessions = %d", code)
	}
	if resp.Sessions.Total != 2 || resp.Sessions.ByState[model.StateCrashed] != 1 || !resp.Complete || resp.HintedEarlierRevisions != 1 || resp.OnEarlierRevisions != 0 ||
		resp.NeedAttention != 1 || len(resp.Attention) != 1 || resp.Attention[0].Name != crashedSession || resp.Attention[0].Kagent == nil ||
		len(resp.Revisions) != 1 || resp.Revisions[0].Template.Name != helperTemplate || !resp.Revisions[0].NewSessions || !resp.Revisions[0].Desired {
		t.Errorf("sessions = %+v", resp.KagentAgentSessions)
	}
	if code := sessions(nil, "spike-agent", nil); code != http.StatusNotFound {
		t.Errorf("an Agent Radar read none of = %d", code)
	}
	if code := sessions(viewer, "k8s-helper", nil); code != http.StatusForbidden {
		t.Errorf("a caller who can't list Agents there = %d", code)
	}
}

func TestSubstrateKagentAgentSessionsLowerBound(t *testing.T) {
	prev := substrateapi.MaxActors
	substrateapi.MaxActors = 2
	t.Cleanup(func() { substrateapi.MaxActors = prev })
	s, _ := kagentServer(t, map[string][]string{"": {"kagent"}})
	var resp substrateKagentSessionsResponse
	if rec := call(t, s, s.handleSubstrateKagentAgentSessions, substrateCall{path: "/s", params: map[string]string{"namespace": "kagent", "name": "k8s-helper"}}, &resp); rec.Code != http.StatusOK {
		t.Fatalf("sessions = %d %s", rec.Code, rec.Body)
	}
	if resp.Complete || resp.Coverage.Actors.Code != substrateapi.CodeCapExceeded || resp.Sources[0] != "atespace" {
		t.Errorf("a capped read = complete %v coverage %+v sources %v", resp.Complete, resp.Coverage.Actors, resp.Sources)
	}
}

// Without kagent served nothing is attributed, the Agents are never read and
// the route says so.
func TestSubstrateKagentNotServed(t *testing.T) {
	s, asked := kagentServer(t, map[string][]string{"": {"kagent"}})
	substrateKagentServed = func() bool { return false }
	agents := substrateKagentAgents
	substrateKagentAgents = func(s *Server, r *http.Request, ns []string) substrateKagentRead {
		if !substrateKagentServed() {
			return substrateKagentRead{}
		}
		return agents(s, r, ns)
	}
	var actors substrateActorsResponse
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?atespace=kagent"}, &actors)
	if actors.KagentAgents != nil || len(*asked) != 0 {
		t.Errorf("agents read without kagent: %v %+v", *asked, actors.KagentAgents)
	}
	for _, r := range actors.Rows {
		if r.Kagent != nil {
			t.Errorf("%s attributed without kagent", r.Name)
		}
	}
	if code := call(t, s, s.handleSubstrateKagentAgentSessions, substrateCall{path: "/s", params: map[string]string{"namespace": "kagent", "name": "k8s-helper"}}, nil).Code; code != http.StatusNotFound {
		t.Errorf("sessions without kagent = %d", code)
	}
}

// With creation times on both sides, a session on an earlier revision whose
// template Substrate created after the Agent counts as the Agent's: the
// shape a kagent Helm change leaves, every Agent on a new revision.
func TestSubstrateKagentEarlierRevisionWithCreationTimes(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{KagentServed: true})
	f := kagentFake()
	created := time.Now().Add(-48 * time.Hour)
	f.templates = append(f.templates, &ateapi.ActorTemplate{Metadata: &ateapi.ResourceMetadata{Atespace: "kagent", Name: "k8s-helper-0123456789ab", CreateTime: timestamppb.New(created.Add(time.Minute))}})
	connectFake(t, s, f, fullToken)
	prevServed, prevAgents := substrateKagentServed, substrateKagentAgents
	t.Cleanup(func() { substrateKagentServed, substrateKagentAgents = prevServed, prevAgents })
	substrateKagentServed = func() bool { return true }
	agent := kagentHelper
	agent.Created = created
	substrateKagentAgents = func(*Server, *http.Request, []string) substrateKagentRead {
		return substrateKagentRead{Served: true, Agents: []model.KagentAgent{agent}, Access: integration.AccessFromScope(nil, false)}
	}
	var actors substrateActorsResponse
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?agent=kagent/k8s-helper"}, &actors)
	r := rowNamed(actors.Rows, earlierSession)
	if actors.Total == nil || *actors.Total != 3 || r == nil || r.Kagent.Kind != model.KagentAgentSession || !r.Kagent.EarlierRevision || r.Kagent.Revision != "0123456789ab" {
		t.Fatalf("the Agent's sessions = total %v, earlier %+v", actors.Total, r)
	}
	var resp substrateKagentSessionsResponse
	call(t, s, s.handleSubstrateKagentAgentSessions, substrateCall{path: "/s", params: map[string]string{"namespace": "kagent", "name": "k8s-helper"}}, &resp)
	if resp.Sessions.Total != 3 || resp.OnEarlierRevisions != 1 || resp.HintedEarlierRevisions != 0 {
		t.Errorf("sessions = %+v", resp.KagentAgentSessions)
	}
}

// With every actor read but the Agents denied or still syncing, an Agent's
// sessions read zero only as a lower bound: the list names the atespaces
// whose Agents it couldn't read, and the coverage says why.
func TestSubstrateKagentFiltersSayWhereAgentsWereNotRead(t *testing.T) {
	s, _ := kagentServer(t, map[string][]string{"": {"kagent"}})
	for _, state := range []string{integration.KindCoverageDenied, integration.KindCoverageSyncing} {
		substrateKagentAgents = func(*Server, *http.Request, []string) substrateKagentRead {
			return substrateKagentRead{Served: true, Access: integration.KindAccess{State: state}}
		}
		for _, q := range []string{"agent=kagent/k8s-helper", "kagent=agentSession", "kagent=unattributed"} {
			var out substrateActorsResponse
			call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?" + q}, &out)
			if out.Coverage.State != substrateapi.CoverageFull || out.KagentAgents == nil || out.KagentAgents.State != state || !slices.Equal(out.KagentUnread, []string{"kagent"}) {
				t.Errorf("%s with Agents %s = actors %s, agents %+v, unread %v", q, state, out.Coverage.State, out.KagentAgents, out.KagentUnread)
			}
		}
		var sandboxes substrateActorsResponse
		call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?kagent=sandbox"}, &sandboxes)
		if sandboxes.KagentUnread != nil || sandboxes.Total == nil || *sandboxes.Total != 1 {
			t.Errorf("sandboxes need no Agent: %v %v", sandboxes.Total, sandboxes.KagentUnread)
		}
	}
	substrateKagentAgents = func(*Server, *http.Request, []string) substrateKagentRead {
		return substrateKagentRead{Served: true, Agents: []model.KagentAgent{kagentHelper}, Access: integration.AccessFromScope(nil, false)}
	}
	var full substrateActorsResponse
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?agent=kagent/k8s-helper"}, &full)
	if full.KagentUnread != nil || *full.Total != 2 {
		t.Errorf("with the Agents read = %v %v", full.Total, full.KagentUnread)
	}
	var other substrateActorsResponse
	call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?agent=research/lit"}, &other)
	if other.KagentUnread != nil {
		t.Errorf("an Agent in a namespace with no kagent sessions = %v", other.KagentUnread)
	}
}

// A session left a hint may run on a template a partial template read
// missed, whose createTime would have proved it the Agent's: the counts are
// then lower bounds, not complete.
func TestSubstrateKagentAgentSessionsNeedEveryTemplateForHints(t *testing.T) {
	prev := substrateapi.MaxListItems
	substrateapi.MaxListItems = 4
	t.Cleanup(func() { substrateapi.MaxListItems = prev })
	s, _ := kagentServer(t, map[string][]string{"": {"kagent"}})
	var resp substrateKagentSessionsResponse
	if rec := call(t, s, s.handleSubstrateKagentAgentSessions, substrateCall{path: "/s", params: map[string]string{"namespace": "kagent", "name": "k8s-helper"}}, &resp); rec.Code != http.StatusOK {
		t.Fatalf("sessions = %d %s", rec.Code, rec.Body)
	}
	if resp.Coverage.Templates.State != substrateapi.CoveragePartial || resp.Coverage.Actors.State != substrateapi.CoverageFull {
		t.Fatalf("fixture: templates %+v actors %+v", resp.Coverage.Templates, resp.Coverage.Actors)
	}
	if resp.HintedEarlierRevisions != 1 || resp.Complete {
		t.Errorf("a hint over a partial template read = hinted %d complete %v", resp.HintedEarlierRevisions, resp.Complete)
	}
}

func TestSubstrateKagentSessionsUnion(t *testing.T) {
	s, _ := kagentServer(t, map[string][]string{"": {"kagent"}})
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"kagent=sessions", 3},
		{"kagent=sessions&namespaces=kagent", 3},
		{"kagent=sessions&namespaces=other", 0},
		{"kagent=sessions&namespace=other", 0},
		{"kagent=sessions&atespace=other", 0},
		{"kagent=sessions&state=crashed", 1},
		{"kagent=sessions&agent=kagent/k8s-helper", 2},
	} {
		var got substrateActorsResponse
		rec := call(t, s, s.handleSubstrateActors, substrateCall{path: "/api/substrate/actors?" + tc.query}, &got)
		if rec.Code != http.StatusOK || got.Total == nil || *got.Total != tc.want {
			t.Fatalf("%s: status %d total=%v", tc.query, rec.Code, got.Total)
		}
		for _, row := range got.Rows {
			if row.Kagent == nil || row.Kagent.Kind == model.KagentSandbox {
				t.Fatalf("non-session row %+v", row)
			}
		}
	}
}

func TestSubstrateKagentSummaryPerCallerAndPaging(t *testing.T) {
	s, _ := kagentServer(t, map[string][]string{"admin": {"kagent"}, "viewer": {}})
	var got kagentSessionSummaryResponse
	rec := call(t, s, s.handleSubstrateKagentSummary, substrateCall{path: "/api/substrate/kagent/summary", user: admin}, &got)
	if rec.Code != http.StatusOK || len(got.Rows) != 1 {
		t.Fatalf("summary status=%d body=%s", rec.Code, rec.Body.String())
	}
	row := got.Rows[0]
	if row.Sessions.Total != 2 || row.Crashed != 1 || row.OnNewSessionsRevision != 2 || row.OnOtherRevisions != 0 || row.HintedEarlierRevisions != 1 || row.Placement.Template == nil || row.Placement.Template.Name != helperTemplate {
		t.Fatalf("summary %+v", row)
	}
	rec = call(t, s, s.handleSubstrateKagentSummary, substrateCall{path: "/api/substrate/kagent/summary", user: viewer}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("denied summary=%d", rec.Code)
	}
	got = kagentSessionSummaryResponse{}
	rec = call(t, s, s.handleSubstrateKagentSummary, substrateCall{path: "/api/substrate/kagent/summary?limit=1&offset=1", user: admin}, &got)
	if rec.Code != http.StatusOK || got.Total != 1 || len(got.Rows) != 0 {
		t.Fatalf("paged summary %+v", got)
	}
}

func TestSubstrateKagentSummarySharesAPITierGate(t *testing.T) {
	s := substrateTestServer(t, substrateapi.Kube{KagentServed: true})
	s.listenAddress = "0.0.0.0"
	for _, handler := range []func(http.ResponseWriter, *http.Request){s.handleSubstrateKagentSummary, s.handleSubstrateActors} {
		if rec := call(t, s, handler, substrateCall{path: "/api/substrate/kagent/summary?kagent=sessions"}, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), substrateapi.CodeSharedRadar) {
			t.Fatalf("shared no-auth: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestSubstrateKagentSummaryRejectsContextSwitch(t *testing.T) {
	s, _ := kagentServer(t, map[string][]string{"": {"kagent"}})
	previous := substrateKagentAgents
	t.Cleanup(func() { substrateKagentAgents = previous })
	substrateKagentAgents = func(s *Server, r *http.Request, ns []string) substrateKagentRead {
		got := previous(s, r, ns)
		k8s.CancelOngoingOperations()
		return got
	}
	rec := call(t, s, s.handleSubstrateKagentSummary, substrateCall{path: "/api/substrate/kagent/summary"}, nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), substrateapi.CodeContextChanged) {
		t.Fatalf("switched summary=%d %s", rec.Code, rec.Body.String())
	}
}

func TestSubstrateKagentSummaryPartialActorsRemainLowerBounds(t *testing.T) {
	old := substrateapi.MaxActors
	substrateapi.MaxActors = 1
	t.Cleanup(func() { substrateapi.MaxActors = old })
	s, _ := kagentServer(t, map[string][]string{"": {"kagent"}})
	var got kagentSessionSummaryResponse
	rec := call(t, s, s.handleSubstrateKagentSummary, substrateCall{path: "/api/substrate/kagent/summary"}, &got)
	if rec.Code != http.StatusOK || len(got.Rows) != 1 || got.Rows[0].Complete || got.Coverage.Actors.Code != substrateapi.CodeCapExceeded {
		t.Fatalf("partial summary=%d %+v", rec.Code, got)
	}
}
