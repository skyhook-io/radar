package agentsandbox

import (
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/readreason"
)

var now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

const ns = "radar-agents-live"

func ago(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }

func cnd(condType, status, reason, message string, age time.Duration) map[string]any {
	return map[string]any{"type": condType, "status": status, "reason": reason, "message": message, "lastTransitionTime": ago(age), "observedGeneration": int64(1)}
}

type objOpt func(map[string]any)

func owner(kind, name string) objOpt {
	return func(o map[string]any) {
		o["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{"apiVersion": ExtensionsGroup + "/v1beta1", "kind": kind, "name": name, "controller": true}}
	}
}

func spec(s map[string]any) objOpt { return func(o map[string]any) { o["spec"] = s } }

func status(s map[string]any) objOpt { return func(o map[string]any) { o["status"] = s } }

func conds(c ...map[string]any) objOpt {
	return func(o map[string]any) {
		st, _ := o["status"].(map[string]any)
		if st == nil {
			st = map[string]any{}
			o["status"] = st
		}
		list := make([]any, len(c))
		for i := range c {
			list[i] = c[i]
		}
		st["conditions"] = list
	}
}

func object(group, kind, namespace, name string, age time.Duration, opts ...objOpt) *unstructured.Unstructured {
	o := map[string]any{
		"apiVersion": group + "/v1beta1", "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": namespace, "generation": int64(1), "creationTimestamp": ago(age)},
	}
	for _, opt := range opts {
		opt(o)
	}
	return &unstructured.Unstructured{Object: o}
}

func sandbox(name string, age time.Duration, opts ...objOpt) *unstructured.Unstructured {
	return object(Group, "Sandbox", ns, name, age, opts...)
}

func claim(name string, age time.Duration, opts ...objOpt) *unstructured.Unstructured {
	return object(ExtensionsGroup, "SandboxClaim", ns, name, age, opts...)
}

func pool(name, template string, desired, ready int64, age time.Duration) *unstructured.Unstructured {
	st := map[string]any{"observedGeneration": int64(1), "replicas": desired}
	if ready > 0 {
		st["readyReplicas"] = ready
	}
	return object(ExtensionsGroup, "SandboxWarmPool", ns, name, age, spec(map[string]any{"replicas": desired, "sandboxTemplateRef": map[string]any{"name": template}}), status(st))
}

func template(name string) *unstructured.Unstructured {
	return object(ExtensionsGroup, "SandboxTemplate", ns, name, 48*time.Hour)
}

func full(items ...*unstructured.Unstructured) Objects {
	return Objects{Items: items, Read: func(string) bool { return true }, Usable: true, Complete: true}
}

func readIn(namespaces []string, items ...*unstructured.Unstructured) Objects {
	return Objects{Items: items, Read: func(n string) bool { return slices.Contains(namespaces, n) }, Usable: true}
}

func denied() Objects { return Objects{Read: func(string) bool { return false }} }

func running() Controller {
	one := int32(1)
	return ReadController(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1}})
}

// The radar-agents-live namespace on the management test cluster, as the
// controller (v1.0.11) wrote it, aged to now.
func liveSandboxes() []*unstructured.Unstructured {
	notSuspended := cnd("Suspended", "False", "NotSuspended", "Sandbox is not suspended", 42*time.Hour)
	unschedulable := "0/4 nodes are available: 4 Too many pods. no new claims to deallocate, preemption: 0/4 nodes are available: 4 No preemption victims found for incoming pod."
	return []*unstructured.Unstructured{
		sandbox("bad-image", 42*time.Hour, conds(notSuspended, cnd("PodScheduled", "True", "PodScheduled", "", 41*time.Hour),
			cnd("Ready", "False", "DependenciesNotReady", `Pod exists with phase: Pending; container "agent" is waiting: ImagePullBackOff`, 42*time.Hour))),
		sandbox("expired", 42*time.Hour, spec(map[string]any{"shutdownTime": ago(43 * time.Hour)}), conds(cnd("Ready", "False", "SandboxExpired", "Sandbox has expired", 42*time.Hour))),
		sandbox("failed", 42*time.Hour, conds(notSuspended, cnd("Ready", "False", "PodFailed", "Pod failed", 42*time.Hour), cnd("Finished", "True", "PodFailed", "Pod failed", 39*time.Hour))),
		sandbox("finished", 42*time.Hour, conds(notSuspended, cnd("Ready", "False", "PodSucceeded", "Pod completed successfully", 41*time.Hour))),
		sandbox("foreign", 42*time.Hour, spec(map[string]any{"operatingMode": "Suspended"}), conds(
			cnd("Suspended", "False", "PodNotOwned", "Refused to delete pod because it is not owned by this sandbox", 42*time.Hour),
			cnd("Ready", "False", "SandboxSuspended", "Sandbox is suspending", 42*time.Hour))),
		sandbox("running", 42*time.Hour, spec(map[string]any{"shutdownTime": ago(39 * time.Hour)}), conds(notSuspended, cnd("Ready", "False", "SandboxExpired", "Sandbox has expired", 39*time.Hour))),
		sandbox("suspended", 42*time.Hour, spec(map[string]any{"operatingMode": "Suspended"}), conds(
			cnd("Suspended", "True", "PodTerminated", "Pod has been terminated. Sandbox is suspended", 42*time.Hour),
			cnd("Ready", "False", "SandboxSuspended", "Sandbox is suspended", 42*time.Hour))),
		sandbox("warm-4xmkm", 23*time.Hour, owner("SandboxWarmPool", "warm"), conds(cnd("PodScheduled", "False", "Unschedulable", unschedulable, 23*time.Hour), cnd("Ready", "False", "DependenciesNotReady", "Pod exists with phase: Pending", 23*time.Hour))),
		sandbox("warm-nd2zw", 17*time.Hour, owner("SandboxWarmPool", "warm"), conds(cnd("PodScheduled", "False", "Unschedulable", unschedulable, 17*time.Hour), cnd("Ready", "False", "DependenciesNotReady", "Pod exists with phase: Pending", 17*time.Hour))),
	}
}

func liveClaims() []*unstructured.Unstructured {
	return []*unstructured.Unstructured{
		claim("claim-missing-pool", 42*time.Hour, spec(map[string]any{"warmPoolRef": map[string]any{"name": "no-such-pool"}}),
			conds(cnd("Ready", "False", "WarmPoolNotFound", `SandboxWarmPool "no-such-pool" not found`, 42*time.Hour))),
		claim("claim-ok", 42*time.Hour, spec(map[string]any{"warmPoolRef": map[string]any{"name": "warm"}, "lifecycle": map[string]any{"shutdownTime": ago(39 * time.Hour)}}),
			conds(cnd("Ready", "False", "ClaimExpired", "Claim expired. Sandbox cleanup initiated.", 42*time.Hour))),
	}
}

func liveInput() Input {
	return Input{
		Now:        now,
		Sandboxes:  full(liveSandboxes()...),
		Claims:     full(liveClaims()...),
		WarmPools:  full(pool("warm", "tmpl", 2, 0, 42*time.Hour)),
		Templates:  full(template("tmpl")),
		Controller: running(),
	}
}

func rowFor(s Summary, kind, name string) *Attention {
	for i := range s.Attention {
		if s.Attention[i].Subject.Kind == kind && s.Attention[i].Subject.Name == name {
			return &s.Attention[i]
		}
	}
	return nil
}

func relation(a *Attention, role string) (Relation, bool) {
	for _, r := range a.Relations {
		if r.Role == role {
			return r, true
		}
	}
	return Relation{}, false
}

func TestLiveNamespace(t *testing.T) {
	s := Summarize(liveInput())

	want := map[string]Rule{
		"Sandbox/bad-image":               RuleFailed,
		"Sandbox/failed":                  RuleFailed,
		"Sandbox/foreign":                 RuleSuspensionBlocked,
		"SandboxClaim/claim-missing-pool": RuleCannotBind,
		"SandboxWarmPool/warm":            RuleReserveBelowTarget,
	}
	got := map[string]Rule{}
	for _, a := range s.Attention {
		got[a.Subject.Kind+"/"+a.Subject.Name] = a.Rule
	}
	if len(got) != len(want) {
		t.Errorf("rows %v, want %v", got, want)
	}
	for k, rule := range want {
		if got[k] != rule {
			t.Errorf("%s: rule %q, want %q", k, got[k], rule)
		}
	}
	// Every row but the pool's is the same finding Issues raise.
	for _, a := range s.Attention {
		if wantIssue := a.Category != CategoryWarmPool; a.Issue != wantIssue {
			t.Errorf("%s: issue=%v", a.ID, a.Issue)
		}
	}

	pod := rowFor(s, "Sandbox", "failed")
	if pod.Title != "Pod failed" || pod.RawDetail != "" {
		t.Errorf("failed: title %q, rawDetail %q (the controller's words only repeat the title)", pod.Title, pod.RawDetail)
	}
	if img := rowFor(s, "Sandbox", "bad-image"); img.Reason != "ImagePullBackOff" || img.Related == nil || img.Related.Refs[0].Kind != "Pod" {
		t.Errorf("bad-image = %+v", img)
	}

	warm := rowFor(s, "SandboxWarmPool", "warm")
	if warm.Title != "Warm reserve below target: 0/2 ready" {
		t.Errorf("warm title %q", warm.Title)
	}
	if !strings.Contains(warm.Reading, "Its 2 sandboxes can’t be scheduled.") || !strings.Contains(warm.Reading, "can’t take a warm one") {
		t.Errorf("warm reading %q", warm.Reading)
	}
	if warm.RawDetailSource != DetailScheduler || !strings.HasPrefix(warm.RawDetail, "0/4 nodes are available") {
		t.Errorf("warm raw detail %q (%s)", warm.RawDetail, warm.RawDetailSource)
	}
	if warm.Related == nil || len(warm.Related.Refs) != 2 {
		t.Errorf("warm related %+v", warm.Related)
	}
	// Its members are on its row, not on their own.
	if rowFor(s, "Sandbox", "warm-4xmkm") != nil {
		t.Error("a pool member got its own row")
	}

	missing := rowFor(s, "SandboxClaim", "claim-missing-pool")
	if r, _ := relation(missing, "warmPool"); r.Presence != Absent || missing.Title != "Warm pool not found" {
		t.Errorf("claim-missing-pool = %+v", missing)
	}

	c := s.Counts
	if c.Sandboxes == nil || *c.Sandboxes != (SandboxCounts{Total: 9, Unschedulable: 2, Suspended: 1, SuspensionBlocked: 1, Expired: 2, Finished: 1, Failed: 2}) {
		t.Errorf("sandbox counts %+v", c.Sandboxes)
	}
	if c.Claims == nil || *c.Claims != (ClaimCounts{Total: 2, Waiting: 1, Expired: 1}) {
		t.Errorf("claim counts %+v", c.Claims)
	}
	if c.WarmPools == nil || *c.WarmPools != (WarmPoolCounts{Total: 1, Ready: 0, Desired: 2}) {
		t.Errorf("pool counts %+v", c.WarmPools)
	}

	if s.Sidebar.Total.Count == nil || *s.Sidebar.Total.Count != 5 || s.Sidebar.Total.LowerBound || s.Sidebar.Controller {
		t.Errorf("sidebar %+v", s.Sidebar)
	}
	if !s.AttentionComplete {
		t.Error("everything was read, yet attention is not complete")
	}

	if len(s.WarmPools) != 1 || s.WarmPools[0].Claims == nil || *s.WarmPools[0].Claims != (ClaimsServed{Total: 1}) || s.WarmPools[0].TemplatePresence != Present {
		t.Errorf("warm pool rows %+v", s.WarmPools)
	}
	if s.Expired.RecentTotal != 3 || s.Expired.Recent[2].Ref.Name != "expired" {
		t.Errorf("expired %+v", s.Expired)
	}
}

// A short reserve says new claims wait only when nothing is ready.
func TestReserveBelowTargetOnlyEmptyReserveDelaysClaims(t *testing.T) {
	in := liveInput()
	members := []*unstructured.Unstructured{sandbox("stuck", 3*time.Hour, owner("SandboxWarmPool", "big"),
		conds(cnd("PodScheduled", "False", "Unschedulable", "0/4 nodes are available: 4 Insufficient cpu.", 3*time.Hour), cnd("Ready", "False", "DependenciesNotReady", "Pod exists with phase: Pending", 3*time.Hour)))}
	for i := 0; i < 9; i++ {
		members = append(members, sandbox("ok-"+string(rune('a'+i)), 3*time.Hour, owner("SandboxWarmPool", "big"), conds(cnd("Ready", "True", "DependenciesReady", "", 3*time.Hour))))
	}
	in.Sandboxes = full(members...)
	in.WarmPools = full(pool("big", "tmpl", 10, 9, 3*time.Hour))
	s := Summarize(in)
	row := rowFor(s, "SandboxWarmPool", "big")
	if row == nil || row.Title != "Warm reserve below target: 9/10 ready" {
		t.Fatalf("row %+v", row)
	}
	if strings.Contains(row.Reading, "claim") || row.Severity == SeverityAlert {
		t.Errorf("nine ready sandboxes, yet: %q (severity %s)", row.Reading, row.Severity)
	}
	if !strings.Contains(row.Reading, "Its sandbox can’t be scheduled.") {
		t.Errorf("reading %q", row.Reading)
	}
}

// A pool refilling: one member is still within its start-up time.
func TestReserveRefillingIsQuiet(t *testing.T) {
	in := liveInput()
	in.Sandboxes = full(sandbox("fresh", time.Minute, owner("SandboxWarmPool", "warm"), conds(cnd("Ready", "False", "DependenciesNotReady", "", time.Minute))))
	if row := rowFor(Summarize(in), "SandboxWarmPool", "warm"); row != nil {
		t.Errorf("a refilling pool raised %+v", row)
	}
}

// The progress clock: advisory after SlowAfter, an Issue after IncidentAfter,
// for every in-progress reading, and for objects the controller never reported.
func TestProgressThresholds(t *testing.T) {
	cases := []struct {
		name      string
		u         *unstructured.Unstructured
		rule      Rule
		issue     bool
		titlePart string
	}{
		{"just started", sandbox("s", 2*time.Minute, conds(cnd("Ready", "False", "DependenciesNotReady", "Pod exists with phase: Pending", 2*time.Minute))), "", false, ""},
		{"slow", sandbox("s", 10*time.Minute, conds(cnd("Ready", "False", "DependenciesNotReady", "Pod exists with phase: Pending", 10*time.Minute))), RuleSlow, false, "Still starting"},
		{"stalled", sandbox("s", 2*time.Hour, conds(cnd("Ready", "False", "DependenciesNotReady", "Pod exists with phase: Pending", 2*time.Hour))), RuleStalled, true, "Still starting"},
		{"never reported", sandbox("s", 40*time.Hour), RuleStalled, true, "No status from the controller"},
		{"claim never reported", claim("c", 40*time.Hour), RuleStalled, true, "No status from the controller"},
		{"claim create pending", claim("c", 40*time.Hour, conds(cnd("Ready", "False", "SandboxCreatePending", "", 40*time.Hour))), RuleStalled, true, "Still starting"},
		{"claim adoption conflict", claim("c", 40*time.Hour, conds(cnd("Ready", "False", "AdoptionConflict", "", 40*time.Hour))), RuleStalled, true, "Still binding"},
		{"claim slow", claim("c", 10*time.Minute, conds(cnd("Ready", "False", "SandboxCreatePending", "", 10*time.Minute))), RuleSlow, false, "Still starting"},
		// A resume: Ready kept its transition from before the suspension.
		{"just resumed", sandbox("s", 48*time.Hour, conds(cnd("Ready", "False", "DependenciesNotReady", "", 48*time.Hour), cnd("Suspended", "False", "NotSuspended", "", time.Minute))), "", false, ""},
		// A suspension has no known start.
		{"suspending", sandbox("s", 48*time.Hour, conds(cnd("Ready", "False", "SandboxSuspended", "", 48*time.Hour), cnd("Suspended", "False", "PodTerminating", "", 48*time.Hour))), "", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := Input{Now: now, Sandboxes: full(), Claims: full(), WarmPools: full(), Templates: full(), Controller: running()}
			if c.u.GetKind() == "Sandbox" {
				in.Sandboxes = full(c.u)
			} else {
				in.Claims = full(c.u)
			}
			s := Summarize(in)
			if c.rule == "" {
				if len(s.Attention) != 0 {
					t.Fatalf("rows %+v", s.Attention)
				}
				return
			}
			if len(s.Attention) != 1 {
				t.Fatalf("rows %+v", s.Attention)
			}
			a := s.Attention[0]
			if a.Rule != c.rule || a.Issue != c.issue || !strings.HasPrefix(a.Title, c.titlePart) || a.Since == nil {
				t.Errorf("row %+v", a)
			}
			if got := IssueRaised(c.u, health.ReadAgentSandbox(c.u), now); got != c.issue {
				t.Errorf("IssueRaised = %v, want %v", got, c.issue)
			}
		})
	}
}

// A claim the caller can read alone keeps a failure its Sandbox forwarded.
func TestForwardedFailureWithSandboxesDenied(t *testing.T) {
	forwarded := claim("bound", time.Hour, spec(map[string]any{"warmPoolRef": map[string]any{"name": "warm"}}),
		status(map[string]any{"sandbox": map[string]any{"name": "warm-abc"}}),
		conds(cnd("Ready", "False", "PodFailed", "Pod failed", time.Hour)))
	in := liveInput()
	in.Claims = full(forwarded)
	in.Sandboxes = denied()
	s := Summarize(in)
	row := rowFor(s, "SandboxClaim", "bound")
	if row == nil || row.Rule != RuleFailed || !row.Issue {
		t.Fatalf("row %+v", row)
	}
	if r, _ := relation(row, "sandbox"); r.Presence != Unread {
		t.Errorf("sandbox relation %+v, want unread", r)
	}
	if s.Counts.Sandboxes != nil {
		t.Errorf("sandbox counts %+v from a denied read", s.Counts.Sandboxes)
	}
	if !s.Sidebar.Sandboxes.LowerBound || s.Sidebar.Sandboxes.Count != nil {
		t.Errorf("sandboxes badge %+v, want unknown", s.Sidebar.Sandboxes)
	}

	// With the Sandbox on screen, its own row says it once.
	in.Sandboxes = full(sandbox("warm-abc", time.Hour, conds(cnd("Ready", "False", "PodFailed", "Pod failed", time.Hour))))
	s = Summarize(in)
	if rowFor(s, "SandboxClaim", "bound") != nil || rowFor(s, "Sandbox", "warm-abc") == nil {
		t.Errorf("rows %+v", s.Attention)
	}
}

// A forwarded state still in progress is the Sandbox's while the Sandbox is read.
func TestForwardedProgress(t *testing.T) {
	forwarded := claim("bound", 2*time.Hour, status(map[string]any{"sandbox": map[string]any{"name": "sb"}}),
		conds(cnd("Ready", "False", "DependenciesNotReady", "Pod exists with phase: Pending", 2*time.Hour)))
	in := Input{Now: now, Sandboxes: full(sandbox("sb", time.Minute, conds(cnd("Ready", "False", "DependenciesNotReady", "", time.Minute)))), Claims: full(forwarded), WarmPools: full(), Templates: full(), Controller: running()}
	if s := Summarize(in); len(s.Attention) != 0 {
		t.Errorf("rows %+v", s.Attention)
	}
	in.Sandboxes = denied()
	s := Summarize(in)
	row := rowFor(s, "SandboxClaim", "bound")
	if row == nil || row.Rule != RuleStalled || row.Issue {
		t.Errorf("row %+v: stalled, and not an Issue (Issues raise it on the Sandbox)", row)
	}
}

// A join over a read with gaps is unread, never absent.
func TestPartialReadsLeaveJoinsUnread(t *testing.T) {
	other := "team-b"
	waiting := object(ExtensionsGroup, "SandboxClaim", other, "waits", 10*time.Hour, spec(map[string]any{"warmPoolRef": map[string]any{"name": "pool-b"}}))
	pb := object(ExtensionsGroup, "SandboxWarmPool", other, "pool-b", 10*time.Hour, spec(map[string]any{"replicas": int64(1), "sandboxTemplateRef": map[string]any{"name": "t"}}), status(map[string]any{"observedGeneration": int64(1), "readyReplicas": int64(1)}))
	in := Input{
		Now:        now,
		Sandboxes:  full(),
		Claims:     full(waiting),
		WarmPools:  readIn([]string{ns}),
		Templates:  readIn([]string{ns}),
		Controller: running(),
	}
	s := Summarize(in)
	row := rowFor(s, "SandboxClaim", "waits")
	if row == nil || row.Rule != RuleStalled {
		t.Fatalf("row %+v, want only the progress clock's row", row)
	}
	if r, _ := relation(row, "warmPool"); r.Presence != Unread {
		t.Errorf("warmPool relation %+v, want unread", r)
	}
	if s.AttentionComplete || !s.Sidebar.Claims.LowerBound || s.Sidebar.Total.Count == nil || !s.Sidebar.Total.LowerBound {
		t.Errorf("complete=%v sidebar=%+v", s.AttentionComplete, s.Sidebar)
	}

	// Read in full there: absent is now a fact.
	in.WarmPools = full()
	if row := rowFor(Summarize(in), "SandboxClaim", "waits"); row == nil || row.Rule != RuleCannotBind || row.Issue {
		t.Errorf("row %+v, want Radar's own cannotBind (not an Issue)", row)
	}

	// Pools read but templates not: the template is unread, never missing.
	in.WarmPools = full(pb)
	in.Templates = readIn([]string{ns})
	in.Claims = denied()
	s = Summarize(in)
	if rowFor(s, "SandboxWarmPool", "pool-b") != nil {
		t.Errorf("rows %+v", s.Attention)
	}
	if len(s.WarmPools) != 1 || s.WarmPools[0].TemplatePresence != Unread || s.WarmPools[0].Claims != nil {
		t.Errorf("pool rows %+v: template unread, claims served unknown", s.WarmPools)
	}
	in.Templates = full()
	if row := rowFor(Summarize(in), "SandboxWarmPool", "pool-b"); row == nil || row.Rule != RuleTemplateMissing || !row.Issue {
		t.Errorf("row %+v", row)
	}
}

func TestStoppedControllerTakesTheLateSandboxes(t *testing.T) {
	in := liveInput()
	in.Sandboxes = full(append(liveSandboxes(), sandbox("late", time.Hour, conds(cnd("Ready", "False", "DependenciesNotReady", "", time.Hour))))...)
	zero := int32(0)
	in.Controller = ReadController(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &zero}})
	s := Summarize(in)
	if len(s.Attention) == 0 || s.Attention[0].Rule != RuleControllerNotRunning || s.Attention[0].Reason != ReasonControllerScaledToZero {
		t.Fatalf("first row %+v", s.Attention)
	}
	if rel := s.Attention[0].Related; rel == nil || len(rel.Refs) != 1 || rel.Refs[0].Name != "late" {
		t.Errorf("controller row related %+v", rel)
	}
	if rowFor(s, "Sandbox", "late") != nil || rowFor(s, "Sandbox", "bad-image") == nil {
		t.Error("late folds into the controller's row; a failure keeps its own")
	}
	if !s.Sidebar.Controller {
		t.Error("sidebar does not say the controller's row is counted")
	}

	in.Controller = ControllerNotRead(readreason.UserForbidden)
	s = Summarize(in)
	if rowFor(s, "Sandbox", "late") == nil || s.AttentionComplete || !s.Sidebar.Total.LowerBound {
		t.Errorf("an unread controller is unknown, never down: %+v", s.Sidebar)
	}
}

func TestReadWarmPool(t *testing.T) {
	for _, c := range []struct {
		name string
		u    *unstructured.Unstructured
		want string
	}{
		{"no status", object(ExtensionsGroup, "SandboxWarmPool", ns, "p", time.Hour, spec(map[string]any{"replicas": int64(2)})), "Pending"},
		{"warming", pool("p", "t", 2, 1, time.Hour), "Warming"},
		{"ready", pool("p", "t", 2, 2, time.Hour), "Ready"},
		{"scaled to zero", pool("p", "t", 0, 0, time.Hour), "Scaled to zero"},
		{"progressing", object(ExtensionsGroup, "SandboxWarmPool", ns, "p", time.Hour, func(o map[string]any) { o["metadata"].(map[string]any)["generation"] = int64(3) }, spec(map[string]any{"replicas": int64(1)}), status(map[string]any{"observedGeneration": int64(2), "readyReplicas": int64(1)})), "Progressing"},
	} {
		if got := ReadWarmPool(c.u).Reason; got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestBadgeUnknownWhenPartialAndEmpty(t *testing.T) {
	in := Input{Now: now, Sandboxes: readIn([]string{ns}), Claims: full(), WarmPools: full(), Templates: full(), Controller: running()}
	s := Summarize(in)
	if s.Sidebar.Sandboxes.Count != nil || !s.Sidebar.Sandboxes.LowerBound {
		t.Errorf("sandboxes badge %+v", s.Sidebar.Sandboxes)
	}
	if s.Counts.Sandboxes == nil || !s.Counts.Sandboxes.LowerBound {
		t.Errorf("sandbox counts %+v, want a lower bound", s.Counts.Sandboxes)
	}
	if s.Sidebar.Claims.Count == nil || *s.Sidebar.Claims.Count != 0 {
		t.Errorf("claims badge %+v: claims were read in full", s.Sidebar.Claims)
	}
}

// Needs attention is one list ordered by severity, then rule, then newest:
// never grouped by kind.
func TestAttentionOrderAcrossKinds(t *testing.T) {
	s := Summarize(liveInput())
	var got []string
	for _, a := range s.Attention {
		got = append(got, a.Subject.Name)
	}
	// failed (39h, newest failure) before bad-image (41h); the alert rows
	// next; the claim the controller can't bind is a warning, so last.
	want := []string{"failed", "bad-image", "foreign", "warm", "claim-missing-pool"}
	if !slices.Equal(got, want) {
		t.Errorf("order %v, want %v", got, want)
	}

	// A stalled Sandbox is an alert, so a claim forwarding a failure (critical)
	// outranks it although the Sandbox list would come first by kind.
	forwarded := claim("forwarded", time.Hour, conds(cnd("Ready", "False", "PodFailed", "Pod failed", time.Hour)))
	in := Input{Now: now, Claims: full(forwarded), WarmPools: full(), Templates: full(), Controller: running(),
		Sandboxes: full(sandbox("late", 2*time.Hour, conds(cnd("Ready", "False", "DependenciesNotReady", "", 2*time.Hour))))}
	s = Summarize(in)
	if len(s.Attention) != 2 || s.Attention[0].Subject.Kind != "SandboxClaim" || s.Attention[1].Rule != RuleStalled {
		t.Errorf("rows %+v", s.Attention)
	}

	// A stopped controller is the most severe row of all.
	zero := int32(0)
	in = liveInput()
	in.Controller = ReadController(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &zero}})
	if s := Summarize(in); s.Attention[0].Category != CategoryController {
		t.Errorf("first row %+v", s.Attention[0])
	}
}

// since is the latest transition among the conditions that report the state;
// absent only where no condition records one.
func TestAttentionSince(t *testing.T) {
	s := Summarize(liveInput())
	at := func(name string) *time.Time {
		for _, a := range s.Attention {
			if a.Subject.Name == name {
				return a.Since
			}
		}
		t.Fatalf("no row for %s", name)
		return nil
	}
	for _, c := range []struct {
		name string
		want time.Duration // ago; 0 = absent
	}{
		// Finished=True moved three hours after Ready went False at creation.
		{"failed", 39 * time.Hour},
		// The image pull can only fail once the Pod was scheduled.
		{"bad-image", 41 * time.Hour},
		{"claim-missing-pool", 42 * time.Hour},
		// Suspended was False before the refusal too: no onset.
		{"foreign", 0},
		// A pool records no condition; its members' times don't say when the reserve fell short.
		{"warm", 0},
	} {
		got := at(c.name)
		switch {
		case c.want == 0 && got != nil:
			t.Errorf("%s: since %v, want absent", c.name, got)
		case c.want != 0 && (got == nil || !got.Equal(now.Add(-c.want))):
			t.Errorf("%s: since %v, want %v", c.name, got, now.Add(-c.want))
		}
	}

	one := int32(1)
	down := ReadController(&appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentAvailable, Status: "False", LastTransitionTime: metav1.NewTime(now.Add(-time.Hour))},
	}}})
	in := liveInput()
	in.Controller = down
	if row := Summarize(in).Attention[0]; row.Category != CategoryController || row.Since == nil || !row.Since.Equal(now.Add(-time.Hour)) {
		t.Errorf("controller row %+v", row)
	}
}

// A pool being deleted creates nothing more: no row, and no Issue either.
func TestDeletingPoolRaisesNothing(t *testing.T) {
	p := pool("going", "gone", 2, 0, time.Hour)
	p.SetDeletionTimestamp(&metav1.Time{Time: now.Add(-time.Minute)})
	in := Input{Now: now, Sandboxes: full(), Claims: full(), WarmPools: full(p), Templates: full(), Controller: running()}
	if s := Summarize(in); len(s.Attention) != 0 {
		t.Errorf("rows %+v", s.Attention)
	}
}

func TestSeverityAndReasons(t *testing.T) {
	s := Summarize(liveInput())
	for name, want := range map[string]Severity{"failed": SeverityCritical, "bad-image": SeverityCritical, "claim-missing-pool": SeverityWarning, "foreign": SeverityAlert, "warm": SeverityAlert} {
		var got Severity
		for _, a := range s.Attention {
			if a.Subject.Name == name {
				got = a.Severity
			}
		}
		if got != want {
			t.Errorf("%s: severity %q, want %q", name, got, want)
		}
	}
	// The controller's own reason stays as it wrote it; Radar's finding is prefixed.
	if r := rowFor(s, "SandboxClaim", "claim-missing-pool"); r.Reason != "WarmPoolNotFound" {
		t.Errorf("controller-reported reason %q", r.Reason)
	}
	in := liveInput()
	in.Claims = full(claim("quiet", time.Hour, spec(map[string]any{"warmPoolRef": map[string]any{"name": "nope"}})))
	if r := rowFor(Summarize(in), "SandboxClaim", "quiet"); r == nil || r.Reason != ReasonWarmPoolNotFound || r.Issue || r.Severity != SeverityWarning {
		t.Errorf("Radar's own finding %+v", r)
	}
	for _, c := range []string{ReasonTemplateNotFound, ReasonWarmPoolNotFound, ReasonNotReported, ReasonControllerNotReady, ReasonControllerScaledToZero} {
		if !strings.HasPrefix(c, "AgentSandbox") {
			t.Errorf("reason %q is not prefixed", c)
		}
	}
}

// Ready and desired are facts only of a Deployment Radar read.
func TestControllerCountsOnlyWhenRead(t *testing.T) {
	if c := running(); c.Ready == nil || *c.Ready != 1 || c.Desired == nil || *c.Desired != 1 {
		t.Errorf("read controller %+v", c)
	}
	for _, c := range []Controller{ControllerNotRead(readreason.KindSyncPending), ControllerMissing()} {
		if c.Ready != nil || c.Desired != nil {
			t.Errorf("%s: ready/desired %v/%v, want absent", c.State, c.Ready, c.Desired)
		}
	}
}

// An expiry with no shutdown time can't be placed in the week; an old one is outside it.
func TestRecentExpiryNeedsADate(t *testing.T) {
	expired := cnd("Ready", "False", "SandboxExpired", "Sandbox has expired", 30*24*time.Hour)
	in := Input{Now: now, Claims: full(), WarmPools: full(), Templates: full(), Controller: running(), Sandboxes: full(
		sandbox("undated", 40*24*time.Hour, conds(expired)),
		sandbox("old", 40*24*time.Hour, spec(map[string]any{"shutdownTime": ago(30 * 24 * time.Hour)}), conds(expired)),
		sandbox("recent", 40*24*time.Hour, spec(map[string]any{"shutdownTime": ago(24 * time.Hour)}), conds(expired)),
		// Expired, then its shutdown time moved forward: not a recent expiry.
		sandbox("moved", 40*24*time.Hour, spec(map[string]any{"shutdownTime": ago(-2 * time.Hour)}), conds(expired)),
	)}
	s := Summarize(in)
	if s.Expired.RecentTotal != 1 || len(s.Expired.Recent) != 1 || s.Expired.Recent[0].Ref.Name != "recent" || !s.Expired.Recent[0].At.Equal(now.Add(-24*time.Hour)) {
		t.Errorf("expired %+v", s.Expired)
	}
	if s.Counts.Sandboxes.Expired != 4 {
		t.Errorf("expired count %d, want all four", s.Counts.Sandboxes.Expired)
	}
}

// The same missing template is one severity on every surface: the pool's row,
// the claim row Radar derives from it, and the pool's Issue (a warning).
func TestMissingTemplateIsAWarning(t *testing.T) {
	waits := claim("waits", time.Hour, spec(map[string]any{"warmPoolRef": map[string]any{"name": "p"}}))
	in := Input{Now: now, Sandboxes: full(), Claims: full(waits), WarmPools: full(pool("p", "gone", 1, 0, time.Hour)), Templates: full(), Controller: running()}
	s := Summarize(in)
	for _, name := range []string{"p", "waits"} {
		var row *Attention
		for i := range s.Attention {
			if s.Attention[i].Subject.Name == name {
				row = &s.Attention[i]
			}
		}
		if row == nil || row.Reason != ReasonTemplateNotFound || row.Severity != SeverityWarning {
			t.Errorf("%s: %+v", name, row)
		}
	}
}

// The row's severity and the Issue's come from one rule.
func TestFailedIsTheSharedRule(t *testing.T) {
	for _, u := range append(liveSandboxes(), liveClaims()...) {
		r := health.ReadAgentSandbox(u)
		want := u.GetKind() == "Sandbox" && r.Verdict.Level == health.LevelUnhealthy
		if got := Failed(u, r); got != want {
			t.Errorf("%s/%s: Failed=%v", u.GetKind(), u.GetName(), got)
		}
	}
	forwarded := claim("c", time.Hour, conds(cnd("Ready", "False", "PodFailed", "Pod failed", time.Hour)))
	if !Failed(forwarded, health.ReadAgentSandbox(forwarded)) {
		t.Error("a forwarded Sandbox failure is terminal on the claim too")
	}
}

// Starting excludes what the scheduler can't place.
func TestUnschedulableCount(t *testing.T) {
	provisioning := sandbox("provisioning", time.Minute, conds(cnd("Ready", "False", "DependenciesNotReady", "Pod exists with phase: Pending", time.Minute)))
	// PodScheduled=True: not a scheduler reading.
	scheduled := sandbox("scheduled", time.Minute, conds(cnd("PodScheduled", "True", "PodScheduled", "", time.Minute), cnd("Ready", "False", "DependenciesNotReady", "", time.Minute)))
	in := Input{Now: now, Sandboxes: full(append(liveSandboxes(), provisioning, scheduled)...), Claims: full(), WarmPools: full(), Templates: full(), Controller: running()}
	if c := Summarize(in).Counts.Sandboxes; c.Unschedulable != 2 || c.Starting != 2 {
		t.Errorf("unschedulable %d, starting %d; want 2 and 2", c.Unschedulable, c.Starting)
	}
}

// A refused suspension's Pod isn't the Sandbox's: its owner is checked before
// anyone removes it, with the one command that shows it.
func TestSuspensionBlockedChecksTheOwnerFirst(t *testing.T) {
	row := rowFor(Summarize(liveInput()), "Sandbox", "foreign")
	cmd := "kubectl get pod foreign -n radar-agents-live -o jsonpath='{.metadata.ownerReferences}'"
	if row == nil || row.NextStep != "Check who owns Pod foreign ("+cmd+") before removing it, or recreate the Sandbox under another name" {
		t.Fatalf("next step %+v", row)
	}
	if row.Command != cmd {
		t.Errorf("command %q, want %q", row.Command, cmd)
	}
	if words := len(strings.Fields(row.NextStep)); words > 25 {
		t.Errorf("next step and command run to %d words", words)
	}
}

// Claims that name a pool with no ready sandbox wait on it: counted on the
// pool's row, and the pool named on each late claim's row. Only claims that are
// unbound and still in progress count; a lower bound where claims were read in part.
func TestEmptyReserveNamesTheClaimsWaitingOnIt(t *testing.T) {
	in := liveInput()
	waits := func(name string) *unstructured.Unstructured {
		return claim(name, 2*time.Hour, spec(map[string]any{"warmPoolRef": map[string]any{"name": "warm"}}), conds(cnd("Ready", "False", "SandboxNotReady", "", 2*time.Hour)))
	}
	in.Claims = full(append(liveClaims(), waits("a"), waits("b"),
		claim("bound", 2*time.Hour, spec(map[string]any{"warmPoolRef": map[string]any{"name": "warm"}}), status(map[string]any{"sandbox": map[string]any{"name": "x"}}), conds(cnd("Ready", "True", "Ready", "", time.Hour))),
	)...)
	s := Summarize(in)
	reserve := rowFor(s, "SandboxWarmPool", "warm")
	if reserve == nil || reserve.WaitingClaims == nil || reserve.WaitingClaims.Count == nil || *reserve.WaitingClaims.Count != 2 || reserve.WaitingClaims.LowerBound {
		t.Fatalf("pool row waiting claims %+v", reserve)
	}
	for _, name := range []string{"a", "b"} {
		if r := rowFor(s, "SandboxClaim", name); r == nil || r.EmptyPool == nil || r.EmptyPool.Name != "warm" {
			t.Errorf("claim %s: %+v, want its empty pool named", name, r)
		}
	}

	// The live namespace's only claim on the pool expired: none waits, and that is known.
	live := rowFor(Summarize(liveInput()), "SandboxWarmPool", "warm")
	if live.WaitingClaims == nil || *live.WaitingClaims.Count != 0 || live.WaitingClaims.LowerBound {
		t.Errorf("live waiting claims %+v", live.WaitingClaims)
	}

	// Claims read elsewhere only: none found here proves nothing.
	in.Claims = readIn([]string{"team-b"})
	if r := rowFor(Summarize(in), "SandboxWarmPool", "warm"); r.WaitingClaims != nil {
		t.Errorf("unread claims counted %+v", r.WaitingClaims)
	}
	in.Claims = readIn([]string{"team-b"}, waits("a"))
	if r := rowFor(Summarize(in), "SandboxWarmPool", "warm"); r.WaitingClaims == nil || *r.WaitingClaims.Count != 1 || !r.WaitingClaims.LowerBound {
		t.Errorf("partly read claims %+v, want a lower bound of 1", r.WaitingClaims)
	}

	// With a sandbox ready the next claim takes it: no claim is said to wait on the reserve.
	in.Claims = full(waits("a"))
	in.WarmPools = full(pool("warm", "tmpl", 3, 1, 42*time.Hour))
	s = Summarize(in)
	if r := rowFor(s, "SandboxWarmPool", "warm"); r != nil && r.WaitingClaims != nil {
		t.Errorf("ready reserve, yet waiting claims %+v", r.WaitingClaims)
	}
	if r := rowFor(s, "SandboxClaim", "a"); r == nil || r.EmptyPool != nil {
		t.Errorf("claim a %+v, want no empty pool", r)
	}
}

// What limits Radar's own reading stays beside the title, not in the folded reading.
func TestCaveatsAreNotFolded(t *testing.T) {
	in := liveInput()
	in.WarmPools = full(pool("warm", "tmpl", 2, 0, 42*time.Hour), pool("no-such-pool", "tmpl", 1, 1, time.Hour))
	row := rowFor(Summarize(in), "SandboxClaim", "claim-missing-pool")
	if row == nil || !strings.Contains(row.Caveat, "may predate it") || row.Reading != "" {
		t.Errorf("claim row %+v, want the contradiction as its caveat", row)
	}

	in = liveInput()
	in.Sandboxes = readIn(nil)
	row = rowFor(Summarize(in), "SandboxWarmPool", "warm")
	if row == nil || row.Caveat != "Radar couldn’t read its sandboxes, so it can’t say why they aren’t ready." || strings.Contains(row.Reading, "couldn’t read") {
		t.Errorf("pool row %+v", row)
	}
}
