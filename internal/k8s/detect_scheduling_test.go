package k8s

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestParseSchedulerMessage_TotalNodesAndPreemptionTail(t *testing.T) {
	msg := "0/5 nodes are available: 2 Insufficient cpu, 3 node(s) had untolerated taint {dedicated: gpu}. " +
		"preemption: 0/5 nodes are available: 5 No preemption victims found for incoming pod."
	total, reasons := parseSchedulerMessage(msg)
	if total != 5 {
		t.Fatalf("totalNodes = %d, want 5", total)
	}
	if len(reasons) != 2 {
		t.Fatalf("got %d reasons, want 2 (preemption tail must be dropped): %+v", len(reasons), reasons)
	}
	if reasons[0].Class != SchedInsufficientResource || reasons[0].Resource != "cpu" || reasons[0].NodeCount != 2 {
		t.Errorf("reason[0] = %+v, want InsufficientResource cpu count=2", reasons[0])
	}
	if reasons[1].Class != SchedUntoleratedTaint || reasons[1].TaintKey != "dedicated" || reasons[1].TaintValue != "gpu" || reasons[1].NodeCount != 3 {
		t.Errorf("reason[1] = %+v, want UntoleratedTaint dedicated=gpu count=3", reasons[1])
	}
}

func TestParseSchedulerMessage_Classes(t *testing.T) {
	cases := []struct {
		name     string
		clause   string // becomes "0/3 nodes are available: <clause>."
		class    SchedReasonClass
		resource string
		taintK   string
		taintV   string
	}{
		{"insufficient cpu", "3 Insufficient cpu", SchedInsufficientResource, "cpu", "", ""},
		{"insufficient memory", "3 Insufficient memory", SchedInsufficientResource, "memory", "", ""},
		{"insufficient gpu", "3 Insufficient nvidia.com/gpu", SchedInsufficientResource, "nvidia.com/gpu", "", ""},
		{"too many pods", "3 Too many pods", SchedInsufficientResource, "pods", "", ""},
		{"node affinity/selector", "3 node(s) didn't match Pod's node affinity/selector", SchedNodeAffinitySelector, "", "", ""},
		{"node affinity only", "3 node(s) didn't match Pod's node affinity", SchedNodeAffinitySelector, "", "", ""},
		{"node selector older", "3 node(s) didn't match node selector", SchedNodeAffinitySelector, "", "", ""},
		{"pod affinity", "3 node(s) didn't match pod affinity rules", SchedPodAffinity, "", "", ""},
		{"pod anti-affinity", "3 node(s) didn't match pod anti-affinity rules", SchedPodAntiAffinity, "", "", ""},
		{"existing anti-affinity", "3 node(s) didn't satisfy existing pods anti-affinity rules", SchedPodAntiAffinity, "", "", ""},
		{"topology spread", "3 node(s) didn't match pod topology spread constraints", SchedTopologySpread, "", "", ""},
		{"volume node affinity", "3 node(s) had volume node affinity conflict", SchedVolumeNodeAffinity, "", "", ""},
		{"volume count", "3 node(s) exceed max volume count", SchedVolumeCount, "", "", ""},
		{"no free ports", "3 node(s) didn't have free ports for the requested pod ports", SchedNoPorts, "", "", ""},
		{"cordoned", "3 node(s) were unschedulable", SchedNodeUnschedulable, "", "", ""},
		{"taint with value", "3 node(s) had untolerated taint {dedicated: gpu}", SchedUntoleratedTaint, "", "dedicated", "gpu"},
		{"taint no value", "3 node(s) had untolerated taint {workload}", SchedUntoleratedTaint, "", "workload", ""},
		{"lifecycle taint reclassified", "3 node(s) had untolerated taint {node.kubernetes.io/not-ready: }", SchedNodeUnschedulable, "", "node.kubernetes.io/not-ready", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, reasons := parseSchedulerMessage("0/3 nodes are available: " + c.clause + ".")
			if len(reasons) != 1 {
				t.Fatalf("got %d reasons, want 1: %+v", len(reasons), reasons)
			}
			r := reasons[0]
			if r.Class != c.class {
				t.Errorf("class = %q, want %q (raw=%q)", r.Class, c.class, r.Raw)
			}
			if c.resource != "" && r.Resource != c.resource {
				t.Errorf("resource = %q, want %q", r.Resource, c.resource)
			}
			if c.taintK != "" && (r.TaintKey != c.taintK || r.TaintValue != c.taintV) {
				t.Errorf("taint = %q=%q, want %q=%q", r.TaintKey, r.TaintValue, c.taintK, c.taintV)
			}
			if r.NodeCount != 3 {
				t.Errorf("nodeCount = %d, want 3", r.NodeCount)
			}
		})
	}
}

func TestParseSchedulerMessage_MultiClauseArchAndTaint(t *testing.T) {
	// The arch-mismatch shape: scheduler reports a selector miss; the
	// node-fit resolver later names kubernetes.io/arch specifically.
	msg := "0/6 nodes are available: 4 node(s) didn't match Pod's node affinity/selector, " +
		"2 node(s) had untolerated taint {dedicated: gpu}."
	total, reasons := parseSchedulerMessage(msg)
	if total != 6 || len(reasons) != 2 {
		t.Fatalf("total=%d reasons=%d, want 6/2: %+v", total, len(reasons), reasons)
	}
	if reasons[0].Class != SchedNodeAffinitySelector || reasons[0].NodeCount != 4 {
		t.Errorf("reason[0] = %+v, want NodeAffinitySelector count=4", reasons[0])
	}
	if reasons[1].Class != SchedUntoleratedTaint || reasons[1].NodeCount != 2 {
		t.Errorf("reason[1] = %+v, want UntoleratedTaint count=2", reasons[1])
	}
}

func TestParseSchedulerMessage_WholeMessageVariants(t *testing.T) {
	total, reasons := parseSchedulerMessage("pod has unbound immediate PersistentVolumeClaims")
	if total != 0 {
		t.Errorf("totalNodes = %d, want 0 (no node prefix)", total)
	}
	if len(reasons) != 1 || reasons[0].Class != SchedVolumeBinding {
		t.Fatalf("want single VolumeBinding reason, got %+v", reasons)
	}
}

func TestDescribeUnschedulable_DropsDRADeallocateJargon(t *testing.T) {
	pod := &corev1.Pod{}
	msg := describeUnschedulable(pod,
		"0/1 nodes are available: no new claims to deallocate,, 1 node(s) didn't have free ports for the requested pod ports.",
		nil)
	if strings.Contains(msg, ",,") {
		t.Fatalf("message should not contain double comma: %q", msg)
	}
	if strings.Contains(msg, "no new claims to deallocate") {
		t.Fatalf("message should drop DRA scheduler jargon: %q", msg)
	}
	if !strings.Contains(msg, "1 node(s) no free host ports") {
		t.Fatalf("message should retain the actionable host-port conflict: %q", msg)
	}

	draOnly := describeUnschedulable(pod,
		"0/1 nodes are available: no new claims to deallocate.",
		nil)
	if strings.Contains(draOnly, "no new claims to deallocate") {
		t.Fatalf("DRA-only message should not fall back to raw scheduler jargon: %q", draOnly)
	}
	if draOnly != "Pod is unschedulable (0/1 nodes available)" {
		t.Fatalf("DRA-only message = %q, want generic node-count summary", draOnly)
	}

	draOnlyWithoutNodeCount := describeUnschedulable(pod, "no new claims to deallocate", nil)
	if draOnlyWithoutNodeCount != "Pod is unschedulable" {
		t.Fatalf("DRA-only message without node count = %q, want generic summary", draOnlyWithoutNodeCount)
	}

	repeatedDRAOnly := describeUnschedulable(pod,
		"scheduler plugin details: no new claims to deallocate, no new claims to deallocate.",
		nil)
	if repeatedDRAOnly != "Pod is unschedulable" {
		t.Fatalf("repeated DRA-only message = %q, want generic summary", repeatedDRAOnly)
	}
}

func TestParseSchedulerMessage_Empty(t *testing.T) {
	if total, reasons := parseSchedulerMessage(""); total != 0 || reasons != nil {
		t.Errorf("empty message should yield 0/nil, got %d/%+v", total, reasons)
	}
	if _, reasons := parseSchedulerMessage("   "); reasons != nil {
		t.Errorf("whitespace message should yield nil reasons, got %+v", reasons)
	}
}

func TestResolveUnsatisfiableNodeSelector_ArchMismatch(t *testing.T) {
	nodes := []NodeFacts{
		{Name: "n1", Labels: map[string]string{"kubernetes.io/arch": "amd64", "kubernetes.io/os": "linux"}},
		{Name: "n2", Labels: map[string]string{"kubernetes.io/arch": "amd64", "kubernetes.io/os": "linux"}},
	}
	p := PodPlacement{NodeSelector: map[string]string{"kubernetes.io/arch": "arm64"}}
	got := resolveUnsatisfiableNodeSelector(p, nodes)
	if len(got) != 1 {
		t.Fatalf("got %d explanations, want 1: %+v", len(got), got)
	}
	// Must name the offending key, the required value, and the fleet's actual value.
	for _, want := range []string{"kubernetes.io/arch", "arm64", "amd64"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("explanation %q missing %q", got[0], want)
		}
	}
}

func TestResolveUnsatisfiableNodeSelector_ZoneViaAffinity(t *testing.T) {
	nodes := []NodeFacts{
		{Name: "n1", Labels: map[string]string{"topology.kubernetes.io/zone": "us-east-1a"}},
		{Name: "n2", Labels: map[string]string{"topology.kubernetes.io/zone": "us-east-1a"}},
	}
	p := PodPlacement{RequiredNodeAffinity: []NodeSelectorTermFacts{
		{Expressions: []MatchExpr{{Key: "topology.kubernetes.io/zone", Operator: "In", Values: []string{"us-east-1b"}}}},
	}}
	got := resolveUnsatisfiableNodeSelector(p, nodes)
	if len(got) != 1 || !strings.Contains(got[0], "topology.kubernetes.io/zone") || !strings.Contains(got[0], "us-east-1a") {
		t.Fatalf("want zone explanation naming the key + fleet value, got %+v", got)
	}
}

func TestResolveUnsatisfiableNodeSelector_MissingLabelEntirely(t *testing.T) {
	nodes := []NodeFacts{{Name: "n1", Labels: map[string]string{"kubernetes.io/arch": "amd64"}}}
	p := PodPlacement{NodeSelector: map[string]string{"disktype": "ssd"}}
	got := resolveUnsatisfiableNodeSelector(p, nodes)
	if len(got) != 1 || !strings.Contains(got[0], "no node carries label disktype") {
		t.Fatalf("want 'no node carries label disktype', got %+v", got)
	}
}

func TestResolveUnsatisfiableNodeSelector_Satisfiable(t *testing.T) {
	nodes := []NodeFacts{
		{Name: "n1", Labels: map[string]string{"kubernetes.io/arch": "amd64"}},
		{Name: "n2", Labels: map[string]string{"kubernetes.io/arch": "arm64"}},
	}
	// arm64 IS present on n2 → no explanation.
	p := PodPlacement{NodeSelector: map[string]string{"kubernetes.io/arch": "arm64"}}
	if got := resolveUnsatisfiableNodeSelector(p, nodes); len(got) != 0 {
		t.Fatalf("satisfiable selector should yield no explanations, got %+v", got)
	}
}

func TestResolveUnsatisfiableNodeSelector_AnyTermSatisfiable(t *testing.T) {
	nodes := []NodeFacts{{Name: "n1", Labels: map[string]string{"pool": "general"}}}
	// Two terms ORed: one matches "general" → affinity is satisfiable, no report.
	p := PodPlacement{RequiredNodeAffinity: []NodeSelectorTermFacts{
		{Expressions: []MatchExpr{{Key: "pool", Operator: "In", Values: []string{"gpu"}}}},
		{Expressions: []MatchExpr{{Key: "pool", Operator: "In", Values: []string{"general"}}}},
	}}
	if got := resolveUnsatisfiableNodeSelector(p, nodes); len(got) != 0 {
		t.Fatalf("one satisfiable term should yield no explanations, got %+v", got)
	}
}

func TestNodeMatchesExpr_Operators(t *testing.T) {
	n := NodeFacts{Labels: map[string]string{"arch": "arm64", "rank": "5"}}
	cases := []struct {
		e    MatchExpr
		want bool
	}{
		{MatchExpr{"arch", "In", []string{"arm64", "amd64"}}, true},
		{MatchExpr{"arch", "In", []string{"amd64"}}, false},
		{MatchExpr{"arch", "NotIn", []string{"amd64"}}, true},
		{MatchExpr{"arch", "Exists", nil}, true},
		{MatchExpr{"gpu", "Exists", nil}, false},
		{MatchExpr{"gpu", "DoesNotExist", nil}, true},
		{MatchExpr{"rank", "Gt", []string{"3"}}, true},
		{MatchExpr{"rank", "Lt", []string{"3"}}, false},
	}
	for _, c := range cases {
		if got := nodeMatchesExpr(n, c.e); got != c.want {
			t.Errorf("nodeMatchesExpr(%+v) = %v, want %v", c.e, got, c.want)
		}
	}
}

func TestDescribeUnschedulable_ArchMismatchNamesLabel(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		NodeSelector: map[string]string{"kubernetes.io/arch": "arm64"},
	}}
	nodes := []NodeFacts{
		{Name: "n1", Labels: map[string]string{"kubernetes.io/arch": "amd64"}},
		{Name: "n2", Labels: map[string]string{"kubernetes.io/arch": "amd64"}},
	}
	msg := describeUnschedulable(pod, "0/2 nodes are available: 2 node(s) didn't match Pod's node affinity/selector.", nodes)
	for _, want := range []string{"kubernetes.io/arch", "arm64", "amd64", "0/2 nodes available"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
	// The resolved label supersedes the generic clause — don't double-report.
	if strings.Contains(msg, "node affinity/selector mismatch") {
		t.Errorf("generic affinity clause should be omitted once resolved: %q", msg)
	}
}

func TestDescribeUnschedulable_ResourcesAndTaint(t *testing.T) {
	pod := &corev1.Pod{}
	msg := describeUnschedulable(pod,
		"0/5 nodes are available: 3 Insufficient cpu, 2 node(s) had untolerated taint {dedicated: gpu}.", nil)
	for _, want := range []string{"insufficient cpu", "untolerated taint dedicated=gpu", "0/5 nodes available"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
}

// TestDescribeUnschedulable_OrdersByBlastRadius pins that the dominant
// constraint (the one that rejected the most nodes) leads the summary, even
// when the scheduler listed a narrower predicate first. Bare pod + nil nodes so
// node-affinity stays unresolved and both clauses flow through summarizeReasons.
func TestDescribeUnschedulable_OrdersByBlastRadius(t *testing.T) {
	msg := describeUnschedulable(&corev1.Pod{},
		"0/3 nodes are available: 1 node(s) didn't satisfy existing pods anti-affinity rules, "+
			"2 node(s) didn't match Pod's node affinity/selector.", nil)
	affinity := strings.Index(msg, "node affinity/selector mismatch")
	antiAffinity := strings.Index(msg, "pod anti-affinity conflict")
	if affinity < 0 || antiAffinity < 0 {
		t.Fatalf("message %q missing an expected clause", msg)
	}
	if affinity > antiAffinity {
		t.Errorf("expected wider node affinity/selector clause (2 nodes) before anti-affinity (1 node): %q", msg)
	}
}

func TestDescribeUnschedulable_FallbackWhenUnparseable(t *testing.T) {
	if got := describeUnschedulable(&corev1.Pod{}, "", nil); got != "Pod is unschedulable" {
		t.Errorf("empty verdict should fall back, got %q", got)
	}
	raw := "some future scheduler phrasing we don't model yet"
	if got := describeUnschedulable(&corev1.Pod{}, raw, nil); got != raw {
		t.Errorf("unmodeled verdict should pass through raw, got %q", got)
	}
}

func TestExtractPodPlacement(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		NodeSelector: map[string]string{"disktype": "ssd"},
		Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      "topology.kubernetes.io/zone",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"us-east-1a"},
					}},
				}},
			},
		}},
	}}
	p := extractPodPlacement(pod)
	if p.NodeSelector["disktype"] != "ssd" {
		t.Errorf("nodeSelector not carried: %+v", p.NodeSelector)
	}
	if len(p.RequiredNodeAffinity) != 1 || len(p.RequiredNodeAffinity[0].Expressions) != 1 {
		t.Fatalf("required affinity not extracted: %+v", p.RequiredNodeAffinity)
	}
	e := p.RequiredNodeAffinity[0].Expressions[0]
	if e.Key != "topology.kubernetes.io/zone" || e.Operator != "In" || len(e.Values) != 1 {
		t.Errorf("expr mismatch: %+v", e)
	}
}

func TestClassifyAdmissionFailure(t *testing.T) {
	cases := []struct {
		msg    string
		reason string
		ok     bool
	}{
		{`Error creating: pods "x" is forbidden: exceeded quota: mem-quota, requested: limits.memory=1Gi, used: limits.memory=2Gi, limited: limits.memory=2Gi`, "QuotaExceeded", true},
		{`Error creating: pods "fix-auth" is forbidden: failed quota: memory-limit-quota: must specify limits.memory`, "QuotaExceeded", true},
		{`Error creating: pods "x" is forbidden: violates PodSecurity "restricted:latest"`, "PodSecurityViolation", true},
		{`Error creating: admission webhook "vpod.example.com" denied the request: nope`, "WebhookDenied", true},
		{`Error creating: pods "x" is forbidden: maximum cpu usage per Container is 1, but limit is 2`, "LimitRangeViolation", true},
		{`Error creating: pods "web-abc" is forbidden: User "system:serviceaccount:prod:web" cannot create resource "pods" in API group "" in the namespace "prod"`, "RBACForbidden", true},
		{`Error creating: object is being deleted: pods "x" already exists`, "", false},
		{`some unrelated message`, "", false},
	}
	for _, c := range cases {
		reason, ok := classifyAdmissionFailure(c.msg)
		if ok != c.ok || reason != c.reason {
			t.Errorf("classifyAdmissionFailure(%.50q) = %q,%v want %q,%v", c.msg, reason, ok, c.reason, c.ok)
		}
	}
}

func TestClassifyPostBindFailure(t *testing.T) {
	cases := []struct {
		reason string
		msg    string
		want   string
		ok     bool
	}{
		{"FailedCreatePodSandBox", `failed to create pod sandbox: ... failed to assign an IP address to container; InsufficientFreeAddresses`, "IPExhaustion", true},
		{"FailedCreatePodSandBox", `failed to create pod sandbox: rpc error: code = Unknown`, "SandboxCreationFailed", true},
		{"FailedAttachVolume", `Multi-Attach error for volume "pvc-123" Volume is already used by pod(s) other-pod`, "VolumeMultiAttach", true},
		{"FailedAttachVolume", `AttachVolume.Attach failed for volume "pvc-123": timed out`, "VolumeAttach", true},
		{"FailedMount", `Unable to attach or mount volumes: timed out waiting for the condition`, "VolumeMount", true},
		{"BackOff", `Back-off restarting failed container`, "", false},
		{"Scheduled", `Successfully assigned default/x to node-1`, "", false},
	}
	for _, c := range cases {
		got, ok := classifyPostBindFailure(c.reason, c.msg)
		if ok != c.ok || got != c.want {
			t.Errorf("classifyPostBindFailure(%q, %.40q) = %q,%v want %q,%v", c.reason, c.msg, got, ok, c.want, c.ok)
		}
	}
}

func TestParseTaintPayload(t *testing.T) {
	cases := map[string]struct{ k, v string }{
		"3 node(s) had untolerated taint {dedicated: gpu}":                   {"dedicated", "gpu"},
		"3 node(s) had untolerated taint {workload}":                         {"workload", ""},
		"3 node(s) had untolerated taint {node.kubernetes.io/unreachable: }": {"node.kubernetes.io/unreachable", ""},
		"3 node(s) had untolerated taint":                                    {"", ""},
	}
	for clause, want := range cases {
		k, v := parseTaintPayload(clause)
		if k != want.k || v != want.v {
			t.Errorf("parseTaintPayload(%q) = %q,%q want %q,%q", clause, k, v, want.k, want.v)
		}
	}
}

func TestUnschedulableAction(t *testing.T) {
	cases := []struct {
		name    string
		reasons []SchedulingReason
		want    string // substring; "" means expect empty (caller falls back)
	}{
		{"empty falls back", nil, ""},
		{"other falls back", []SchedulingReason{{Class: SchedOther}}, ""},
		{"insufficient cpu names the resource", []SchedulingReason{{Class: SchedInsufficientResource, Resource: "cpu", NodeCount: 3}}, "lower the pod's cpu request"},
		{"max-pods is not a request problem", []SchedulingReason{{Class: SchedInsufficientResource, Resource: "pods", NodeCount: 3}}, "max-pods"},
		{"untolerated taint names the key", []SchedulingReason{{Class: SchedUntoleratedTaint, TaintKey: "dedicated", NodeCount: 2}}, `taint "dedicated"`},
		{"volume binding distinct from node affinity", []SchedulingReason{{Class: SchedVolumeBinding, NodeCount: 1}}, "StorageClass/provisioner"},
		{"dominant reason wins by node count", []SchedulingReason{
			{Class: SchedUntoleratedTaint, TaintKey: "a", NodeCount: 1},
			{Class: SchedInsufficientResource, Resource: "memory", NodeCount: 5},
		}, "memory"},
		{"multiple classes are acknowledged", []SchedulingReason{
			{Class: SchedInsufficientResource, Resource: "memory", NodeCount: 5},
			{Class: SchedUntoleratedTaint, TaintKey: "a", NodeCount: 1},
		}, "More than one constraint"},
		{"high-count Other doesn't bury a low-count actionable reason", []SchedulingReason{
			{Class: SchedOther, NodeCount: 9},
			{Class: SchedVolumeBinding, NodeCount: 1},
		}, "StorageClass/provisioner"},
	}
	for _, c := range cases {
		got := unschedulableAction(c.reasons)
		if c.want == "" {
			if got != "" {
				t.Errorf("%s: want empty, got %q", c.name, got)
			}
			continue
		}
		if !hasSubstr(got, c.want) {
			t.Errorf("%s: action %q missing %q", c.name, got, c.want)
		}
	}
}

func TestDiagnoseUnschedulableSharesOneParse(t *testing.T) {
	// Message + Action must come from the same verdict: a taint verdict yields a
	// taint message AND a taint action, never a generic fallback action.
	msg, action := diagnoseUnschedulable(&corev1.Pod{},
		"0/3 nodes are available: 3 node(s) had untolerated taint {dedicated: gpu}.", nil)
	if !hasSubstr(action, "toleration") {
		t.Errorf("taint verdict should yield a taint action, got %q", action)
	}
	if action == genericUnschedulableAction {
		t.Errorf("expected a targeted action, got the generic fallback")
	}
	if msg == "" {
		t.Errorf("expected a non-empty message")
	}
	// Unparseable verdict → generic fallback action, still non-empty.
	_, fallback := diagnoseUnschedulable(&corev1.Pod{}, "something the parser doesn't recognize", nil)
	if fallback != genericUnschedulableAction {
		t.Errorf("unrecognized verdict should fall back to generic action, got %q", fallback)
	}
}
