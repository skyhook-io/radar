package topology

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utiljson "k8s.io/apimachinery/pkg/util/json"
)

var jobSetTestGVR = schema.GroupVersionResource{Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}

func jobSetTestDynamic(jobSets ...*unstructured.Unstructured) *genericIdentityDynamic {
	return &genericIdentityDynamic{
		watched:   []schema.GroupVersionResource{jobSetTestGVR},
		kinds:     map[schema.GroupVersionResource]string{jobSetTestGVR: "JobSet"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{jobSetTestGVR: jobSets},
		listCalls: map[schema.GroupVersionResource]int{},
	}
}

func testJobSetRoot(namespace, name, uid string, spec, status map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "jobset.x-k8s.io/v1alpha2",
		"kind":       "JobSet",
		"metadata":   map[string]any{"name": name, "namespace": namespace, "uid": uid},
	}
	if spec != nil {
		obj["spec"] = spec
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

func jobSetOwnerRef(name, uid string) metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{APIVersion: "jobset.x-k8s.io/v1alpha2", Kind: "JobSet", Name: name, UID: types.UID(uid), Controller: &controller}
}

func memberJob(namespace, name, uid string, owners ...metav1.OwnerReference) *batchv1.Job {
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(uid), OwnerReferences: owners}}
}

func jobPod(namespace, name, jobName, jobUID string) *corev1.Pod {
	controller := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace, Name: name, UID: types.UID("pod-" + name),
			Labels:          map[string]string{"job-name": jobName},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: jobName, UID: types.UID(jobUID), Controller: &controller}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func buildJobSetTopology(t *testing.T, provider *mockProvider, dynamic DynamicProvider, opts BuildOptions) *Topology {
	t.Helper()
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	return topo
}

func managesTargets(topo *Topology, source string) []string {
	var out []string
	for _, e := range topo.Edges {
		if e.Source == source && e.Type == EdgeManages {
			out = append(out, e.Target)
		}
	}
	sort.Strings(out)
	return out
}

func edgeByID(edges []Edge, id string) *Edge {
	for i := range edges {
		if edges[i].ID == id {
			return &edges[i]
		}
	}
	return nil
}

func TestJobSetNodeWithoutChildren(t *testing.T) {
	cases := []struct {
		name       string
		spec       map[string]any
		status     map[string]any
		wantStatus HealthStatus
		wantPhase  string
	}{
		{"nothing observed yet", nil, nil, StatusUnknown, "Unknown"},
		{"observed with no child Jobs", nil, map[string]any{"replicatedJobsStatus": []any{map[string]any{"name": "workers"}}}, StatusNeutral, "Pending"},
		{"suspended", map[string]any{"suspend": true}, map[string]any{"conditions": []any{map[string]any{"type": "Suspended", "status": "True"}}}, StatusNeutral, "Suspended"},
		{"completed and cleaned up", nil, map[string]any{"terminalState": "Completed", "conditions": []any{map[string]any{"type": "Completed", "status": "True"}}}, StatusNeutral, "Completed"},
		{"failed", nil, map[string]any{"terminalState": "Failed"}, StatusUnhealthy, "Failed"},
		{"recreating", nil, map[string]any{"conditions": []any{map[string]any{"type": "RestartingJobSet", "status": "True"}}}, StatusDegraded, "Restarting"},
		{"starting in order", nil, map[string]any{"conditions": []any{map[string]any{"type": "StartupPolicyInProgress", "status": "True"}}}, StatusDegraded, "Starting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := map[string]any{"replicatedJobs": []any{map[string]any{"name": "workers", "replicas": int64(3)}}}
			for k, v := range tc.spec {
				spec[k] = v
			}
			root := testJobSetRoot("ml", "train", "jobset-uid", spec, tc.status)
			topo := buildJobSetTopology(t, &mockProvider{}, jobSetTestDynamic(root), DefaultBuildOptions())

			node := nodeByID(topo.Nodes, "jobset/ml/train/jobset.x-k8s.io")
			if node == nil {
				t.Fatalf("JobSet without children has no node: %+v", topo.Nodes)
			}
			if node.Kind != KindJobSet || node.Status != tc.wantStatus || node.Data["phase"] != tc.wantPhase {
				t.Fatalf("node = kind %s status %s phase %v, want JobSet %s %s", node.Kind, node.Status, node.Data["phase"], tc.wantStatus, tc.wantPhase)
			}
			if node.Data["declaredJobs"] != int64(3) {
				t.Fatalf("declaredJobs = %v, want 3", node.Data["declaredJobs"])
			}
			if targets := managesTargets(topo, node.ID); len(targets) != 0 {
				t.Fatalf("childless JobSet manages %v", targets)
			}
		})
	}
}

type topologyMembershipVectors struct {
	JobSet  map[string]any `json:"jobSet"`
	Jobs    []batchv1.Job  `json:"jobs"`
	Members []string       `json:"members"`
}

// The topology must draw exactly the members the JobSet member API lists;
// internal/server checks jobSetMemberRuns against the same vectors.
func TestJobSetEdgesAgreeWithMembershipVectors(t *testing.T) {
	raw, err := os.ReadFile("../jobset/testdata/membership.json")
	if err != nil {
		t.Fatal(err)
	}
	var v topologyMembershipVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	root := &unstructured.Unstructured{Object: v.JobSet}
	provider := &mockProvider{}
	for i := range v.Jobs {
		provider.jobs = append(provider.jobs, &v.Jobs[i])
	}
	topo := buildJobSetTopology(t, provider, jobSetTestDynamic(root), DefaultBuildOptions())

	var want []string
	for _, name := range v.Members {
		want = append(want, fmt.Sprintf("job/%s/%s", root.GetNamespace(), name))
	}
	sort.Strings(want)
	if got := managesTargets(topo, "jobset/ml/train/jobset.x-k8s.io"); !slices.Equal(got, want) {
		t.Fatalf("JobSet → Job edges = %v, want %v", got, want)
	}
	// Non-members stay drawn, as roots of their own.
	for _, job := range v.Jobs {
		if nodeByID(topo.Nodes, fmt.Sprintf("job/%s/%s", job.Namespace, job.Name)) == nil {
			t.Fatalf("Job %s/%s missing from the graph", job.Namespace, job.Name)
		}
	}
}

func TestJobSetPodShortcutsFollowExactMemberJob(t *testing.T) {
	root := testJobSetRoot("ml", "train", "jobset-uid", nil, nil)
	current := memberJob("ml", "train-workers-0", "job-current", jobSetOwnerRef("train", "jobset-uid"))
	provider := &mockProvider{
		jobs: []*batchv1.Job{current},
		pods: []*corev1.Pod{
			jobPod("ml", "train-workers-0-abc", "train-workers-0", "job-current"),
			// Left over from an earlier Job with the same name.
			jobPod("ml", "train-workers-0-old", "train-workers-0", "job-earlier"),
		},
	}
	topo := buildJobSetTopology(t, provider, jobSetTestDynamic(root), DefaultBuildOptions())

	shortcut := edgeByID(topo.Edges, "jobset/ml/train/jobset.x-k8s.io-to-pod/ml/train-workers-0-abc-shortcut")
	if shortcut == nil {
		t.Fatalf("missing JobSet → Pod shortcut: %+v", topo.Edges)
	}
	if shortcut.Type != EdgeManages || shortcut.SkipIfKindVisible != string(KindJob) {
		t.Fatalf("shortcut = %+v, want a manages edge hidden while Jobs are visible", shortcut)
	}
	if edgeByID(topo.Edges, "jobset/ml/train/jobset.x-k8s.io-to-pod/ml/train-workers-0-old-shortcut") != nil {
		t.Fatal("a Pod of an earlier same-named Job must not attach to the JobSet")
	}
	if edgeByID(topo.Edges, "jobset/ml/train/jobset.x-k8s.io-to-job/ml/train-workers-0") == nil {
		t.Fatal("missing JobSet → Job edge")
	}
	// With Jobs shown, the visible path is JobSet → Job → Pod: the earlier
	// Job's Pod must not hang off the current Job either.
	if edgeByID(topo.Edges, "job/ml/train-workers-0-to-pod/ml/train-workers-0-abc") == nil {
		t.Fatal("missing Job → Pod edge for the current Job's Pod")
	}
	for _, e := range topo.Edges {
		if e.Target == "pod/ml/train-workers-0-old" && e.Type == EdgeManages {
			t.Fatalf("earlier Job's Pod linked: %+v", e)
		}
	}
}

// In a PodGroup, an earlier incarnation's Pod must not be the one that
// decides the group's owner edges.
func TestJobSetPodGroupWithEarlierIncarnationKeepsCurrentEdges(t *testing.T) {
	root := testJobSetRoot("ml", "train", "jobset-uid", nil, nil)
	provider := &mockProvider{jobs: []*batchv1.Job{memberJob("ml", "train-workers-0", "job-current", jobSetOwnerRef("train", "jobset-uid"))}}
	// The earlier incarnation's Pod sorts first.
	provider.pods = append(provider.pods, jobPod("ml", "train-workers-0-aaa-old", "train-workers-0", "job-earlier"))
	for i := 0; i < 4; i++ {
		provider.pods = append(provider.pods, jobPod("ml", fmt.Sprintf("train-workers-0-b%d", i), "train-workers-0", "job-current"))
	}
	opts := DefaultBuildOptions()
	opts.MaxIndividualPods = 2
	topo := buildJobSetTopology(t, provider, jobSetTestDynamic(root), opts)

	var group *Node
	for i := range topo.Nodes {
		if topo.Nodes[i].Kind == KindPodGroup {
			group = &topo.Nodes[i]
		}
	}
	if group == nil {
		t.Fatalf("no PodGroup: %+v", topo.Nodes)
	}
	if edgeByID(topo.Edges, "job/ml/train-workers-0-to-"+group.ID) == nil {
		t.Fatalf("current Job lost its PodGroup edge: %+v", topo.Edges)
	}
	if edgeByID(topo.Edges, "jobset/ml/train/jobset.x-k8s.io-to-"+group.ID+"-shortcut") == nil {
		t.Fatalf("JobSet lost its PodGroup shortcut: %+v", topo.Edges)
	}
	// Expanding the group connects each Pod to its ownerIds, and to every
	// owner of the group when ownerIds is absent; the earlier incarnation's
	// Pod must say "none" explicitly.
	for _, pd := range group.Data["pods"].([]map[string]any) {
		owners, present := pd["ownerIds"].([]string)
		if pd["name"] == "train-workers-0-aaa-old" {
			if !present || len(owners) != 0 {
				t.Fatalf("earlier incarnation's Pod ownerIds = %#v, want explicitly empty", pd["ownerIds"])
			}
			continue
		}
		if !slices.Contains(owners, "job/ml/train-workers-0") {
			t.Fatalf("current Pod ownerIds = %#v, want the current Job", pd["ownerIds"])
		}
	}
}

func TestJobSetShortcutReachesPodGroup(t *testing.T) {
	root := testJobSetRoot("ml", "train", "jobset-uid", nil, nil)
	provider := &mockProvider{jobs: []*batchv1.Job{memberJob("ml", "train-workers-0", "job-uid", jobSetOwnerRef("train", "jobset-uid"))}}
	for i := 0; i < 8; i++ {
		provider.pods = append(provider.pods, jobPod("ml", fmt.Sprintf("train-workers-0-%d", i), "train-workers-0", "job-uid"))
	}
	opts := DefaultBuildOptions()
	opts.MaxIndividualPods = 2
	topo := buildJobSetTopology(t, provider, jobSetTestDynamic(root), opts)

	var groups []string
	for _, n := range topo.Nodes {
		if n.Kind == KindPodGroup {
			groups = append(groups, n.ID)
		}
	}
	if len(groups) != 1 {
		t.Fatalf("want one PodGroup, got %v", groups)
	}
	shortcut := edgeByID(topo.Edges, "jobset/ml/train/jobset.x-k8s.io-to-"+groups[0]+"-shortcut")
	if shortcut == nil || shortcut.SkipIfKindVisible != string(KindJob) {
		t.Fatalf("missing JobSet → PodGroup shortcut: %+v", topo.Edges)
	}
}

func TestJobSetNamespaceFilter(t *testing.T) {
	inScope := testJobSetRoot("ml", "train", "uid-a", nil, nil)
	outOfScope := testJobSetRoot("other", "train", "uid-b", nil, nil)
	provider := &mockProvider{jobs: []*batchv1.Job{
		memberJob("ml", "train-a", "job-a", jobSetOwnerRef("train", "uid-a")),
		memberJob("other", "train-b", "job-b", jobSetOwnerRef("train", "uid-b")),
	}}
	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"ml"}
	topo := buildJobSetTopology(t, provider, jobSetTestDynamic(inScope, outOfScope), opts)

	if nodeByID(topo.Nodes, "jobset/other/train/jobset.x-k8s.io") != nil {
		t.Fatal("JobSet outside the namespace filter was drawn")
	}
	if got := managesTargets(topo, "jobset/ml/train/jobset.x-k8s.io"); !slices.Equal(got, []string{"job/ml/train-a"}) {
		t.Fatalf("in-scope edges = %v", got)
	}
	for _, e := range topo.Edges {
		if e.Source == "jobset/other/train/jobset.x-k8s.io" || e.Target == "job/other/train-b" {
			t.Fatalf("out-of-scope edge drawn: %+v", e)
		}
	}
}

func TestJobSetForeignGroupIsNotTheTypedPass(t *testing.T) {
	foreignGVR := schema.GroupVersionResource{Group: "jobset.example.com", Version: "v1", Resource: "jobsets"}
	foreign := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "jobset.example.com/v1", "kind": "JobSet",
		"metadata": map[string]any{"name": "train", "namespace": "ml", "uid": "foreign-uid"},
	}}
	controller := true
	dynamic := &genericIdentityDynamic{
		watched:   []schema.GroupVersionResource{foreignGVR},
		kinds:     map[schema.GroupVersionResource]string{foreignGVR: "JobSet"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{foreignGVR: {foreign}},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	provider := &mockProvider{jobs: []*batchv1.Job{memberJob("ml", "train-0", "job-uid", metav1.OwnerReference{
		APIVersion: "jobset.example.com/v1", Kind: "JobSet", Name: "train", UID: "foreign-uid", Controller: &controller,
	})}}
	topo := buildJobSetTopology(t, provider, dynamic, DefaultBuildOptions())

	for _, n := range topo.Nodes {
		if n.Kind == KindJobSet {
			t.Fatalf("a foreign-group JobSet became a JobSet root: %+v", n)
		}
	}
	for _, e := range topo.Edges {
		if e.Target == "job/ml/train-0" {
			t.Fatalf("a foreign-group owner reached the Job: %+v", e)
		}
	}
}

// A JobSet another object owns (a Kubeflow TrainJob) stays one node: when the
// owner is drawn, the generic pass adds only the owner's edge to it.
func TestJobSetOwnedByDrawnOwnerIsNotDuplicated(t *testing.T) {
	trainJobGVR := schema.GroupVersionResource{Group: "trainer.kubeflow.org", Version: "v1alpha1", Resource: "trainjobs"}
	root := testJobSetRoot("ml", "train", "jobset-uid", nil, nil)
	root.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Name: "train", UID: "trainjob-uid"}})
	dynamic := &genericIdentityDynamic{
		watched:   []schema.GroupVersionResource{jobSetTestGVR, trainJobGVR},
		kinds:     map[schema.GroupVersionResource]string{jobSetTestGVR: "JobSet", trainJobGVR: "TrainJob"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{jobSetTestGVR: {root}},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	b := &Builder{dynamic: dynamic}
	nodes, warnings, _ := b.addJobSetNodes(nil, nil, DefaultBuildOptions())
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	nodes = append(nodes, Node{ID: "trainjob/ml/train/trainer.kubeflow.org", Kind: NodeKind("TrainJob"), Name: "train", Data: map[string]any{"namespace": "ml", "apiVersion": "trainer.kubeflow.org/v1alpha1"}})

	nodes, edges := b.addGenericCRDNodes(nodes, nil, DefaultBuildOptions())

	var jobSets int
	for _, n := range nodes {
		if n.Kind == KindJobSet || string(n.Kind) == "JobSet" {
			jobSets++
		}
	}
	if jobSets != 1 {
		t.Fatalf("JobSet drawn %d times: %+v", jobSets, nodes)
	}
	if len(edges) != 1 || edges[0].Source != "trainjob/ml/train/trainer.kubeflow.org" || edges[0].Target != "jobset/ml/train/jobset.x-k8s.io" {
		t.Fatalf("owner edges = %+v, want TrainJob → JobSet only", edges)
	}
}

func TestJobSetMemberRelationships(t *testing.T) {
	root := testJobSetRoot("ml", "train", "jobset-uid", nil, nil)
	provider := &mockProvider{
		jobs: []*batchv1.Job{memberJob("ml", "train-0", "job-uid", jobSetOwnerRef("train", "jobset-uid"))},
		pods: []*corev1.Pod{jobPod("ml", "train-0-abc", "train-0", "job-uid")},
	}
	dynamic := jobSetTestDynamic(root)
	topo := buildJobSetTopology(t, provider, dynamic, DefaultBuildOptions())
	index := IndexByResource(topo)
	want := ResourceRef{Group: "jobset.x-k8s.io", Kind: "JobSet", Namespace: "ml", Name: "train"}

	// The legacy managedBy pointer names the topmost manager: the JobSet,
	// for the member Job and for its Pod.
	rel := GetRelationshipsWithObject("Job", "ml", "train-0", provider.jobs[0], topo, provider, dynamic, index)
	if rel == nil || len(rel.ManagedBy) != 1 || rel.ManagedBy[0] != want {
		t.Fatalf("member Job managedBy = %+v, want %+v", rel, want)
	}
	rel = GetRelationshipsWithObject("Pod", "ml", "train-0-abc", provider.pods[0], topo, provider, dynamic, index)
	if rel == nil || len(rel.ManagedBy) != 1 || rel.ManagedBy[0] != want {
		t.Fatalf("member Pod managedBy = %+v, want %+v", rel, want)
	}
}

// The node's status and label match the JobSet status badge on the same
// vectors (packages/k8s-ui resource-utils-jobset-lws.test.ts reads them too).
func TestJobSetNodeStatusMatchesSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("../jobset/testdata/lifecycle.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Name   string         `json:"name"`
			JobSet map[string]any `json:"jobSet"`
			Label  string         `json:"label"`
			Level  HealthStatus   `json:"level"`
		} `json:"cases"`
	}
	// The apimachinery decoder keeps integers int64, as the API client does.
	if err := utiljson.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, tc := range file.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			node := jobSetNode(&unstructured.Unstructured{Object: tc.JobSet})
			if node.Status != tc.Level || node.Data["phase"] != tc.Label {
				t.Fatalf("node = %s %v, want %s %s", node.Status, node.Data["phase"], tc.Level, tc.Label)
			}
		})
	}
}

// A JobSet's Kueue Workload attaches through the generic owner pass once the
// JobSet is drawn, as a Job's does.
func TestJobSetOwnedWorkloadAttachesThroughGenericPass(t *testing.T) {
	workloadGVR := schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "workloads"}
	root := testJobSetRoot("ml", "train", "jobset-uid", nil, nil)
	workload := genericIdentityObject(workloadGVR, "Workload", "ml", "jobset-train-abc12", jobSetOwnerRef("train", "jobset-uid"))
	dynamic := &genericIdentityDynamic{
		watched:   []schema.GroupVersionResource{jobSetTestGVR, workloadGVR},
		kinds:     map[schema.GroupVersionResource]string{jobSetTestGVR: "JobSet", workloadGVR: "Workload"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{jobSetTestGVR: {root}, workloadGVR: {workload}},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	topo := buildJobSetTopology(t, &mockProvider{}, dynamic, DefaultBuildOptions())

	if got := managesTargets(topo, "jobset/ml/train/jobset.x-k8s.io"); !slices.Equal(got, []string{"workload/ml/jobset-train-abc12/kueue.x-k8s.io"}) {
		t.Fatalf("JobSet manages %v, want its Workload", got)
	}
	var jobSets int
	for _, n := range topo.Nodes {
		if n.Kind == KindJobSet {
			jobSets++
		}
	}
	if jobSets != 1 {
		t.Fatalf("JobSet drawn %d times", jobSets)
	}
}

// A root TrainJob isn't drawn (the generic pass only adds owned objects), so
// its JobSet is still one node, with no owner edge.
func TestJobSetUnderUndrawnTrainJobIsOneRoot(t *testing.T) {
	trainJobGVR := schema.GroupVersionResource{Group: "trainer.kubeflow.org", Version: "v1alpha1", Resource: "trainjobs"}
	root := testJobSetRoot("ml", "train", "jobset-uid", nil, nil)
	root.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Name: "train", UID: "trainjob-uid"}})
	trainJob := genericIdentityObject(trainJobGVR, "TrainJob", "ml", "train")
	dynamic := &genericIdentityDynamic{
		watched:   []schema.GroupVersionResource{jobSetTestGVR, trainJobGVR},
		kinds:     map[schema.GroupVersionResource]string{jobSetTestGVR: "JobSet", trainJobGVR: "TrainJob"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{jobSetTestGVR: {root}, trainJobGVR: {trainJob}},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	topo := buildJobSetTopology(t, &mockProvider{}, dynamic, DefaultBuildOptions())

	var jobSets int
	for _, n := range topo.Nodes {
		if n.Kind == KindJobSet {
			jobSets++
		}
		if n.Kind == NodeKind("TrainJob") {
			t.Fatalf("root TrainJob drawn: %+v", n)
		}
	}
	if jobSets != 1 {
		t.Fatalf("JobSet drawn %d times", jobSets)
	}
	for _, e := range topo.Edges {
		if e.Target == "jobset/ml/train/jobset.x-k8s.io" {
			t.Fatalf("unexpected edge into the JobSet: %+v", e)
		}
	}
}

func TestJobSetUninterpretedVersionWarns(t *testing.T) {
	beta := schema.GroupVersionResource{Group: "jobset.x-k8s.io", Version: "v1beta1", Resource: "jobsets"}
	root := testJobSetRoot("ml", "train", "jobset-uid", nil, map[string]any{"terminalState": "Failed"})
	root.SetAPIVersion("jobset.x-k8s.io/v1beta1")
	dynamic := &genericIdentityDynamic{
		watched:   []schema.GroupVersionResource{beta},
		kinds:     map[schema.GroupVersionResource]string{beta: "JobSet"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{beta: {root}},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	provider := &mockProvider{jobs: []*batchv1.Job{memberJob("ml", "train-0", "job-uid", metav1.OwnerReference{
		APIVersion: "jobset.x-k8s.io/v1beta1", Kind: "JobSet", Name: "train", UID: "jobset-uid", Controller: ptrTo(true),
	})}}
	topo := buildJobSetTopology(t, provider, dynamic, DefaultBuildOptions())

	node := nodeByID(topo.Nodes, "jobset/ml/train/jobset.x-k8s.io")
	if node == nil || node.Status != StatusUnknown {
		t.Fatalf("uninterpreted JobSet = %+v, want an unknown-status node", node)
	}
	if targets := managesTargets(topo, node.ID); len(targets) != 0 {
		t.Fatalf("uninterpreted JobSet manages %v", targets)
	}
	if !slices.ContainsFunc(topo.Warnings, func(w string) bool { return strings.Contains(w, "jobset.x-k8s.io/v1beta1") }) {
		t.Fatalf("no warning names the uninterpreted version: %v", topo.Warnings)
	}
}

func ptrTo[T any](v T) *T { return &v }

// The incarnation check holds for every Job, not only JobSet members: a Pod
// left from an earlier Job of the same name (a recreated hook Job, say) isn't
// the current Job's, in the graph or in its summary-mode health.
func TestPlainJobIgnoresEarlierIncarnationPods(t *testing.T) {
	job := memberJob("ops", "migrate", "job-current")
	provider := &mockProvider{
		jobs: []*batchv1.Job{job},
		pods: []*corev1.Pod{
			jobPod("ops", "migrate-new", "migrate", "job-current"),
			jobPod("ops", "migrate-old", "migrate", "job-earlier"),
		},
	}
	topo := buildJobSetTopology(t, provider, nil, DefaultBuildOptions())
	if got := managesTargets(topo, "job/ops/migrate"); !slices.Equal(got, []string{"pod/ops/migrate-new"}) {
		t.Fatalf("Job → Pod edges = %v, want only the current Job's Pod", got)
	}

	opts := DefaultBuildOptions()
	opts.Namespaces = []string{"ops"}
	opts.SummaryMode = true
	topo = buildJobSetTopology(t, provider, nil, opts)
	summary, _ := nodeByID(topo.Nodes, "job/ops/migrate").Data["podSummary"].(map[string]any)
	if summary["total"] != 1 {
		t.Fatalf("summary-mode Job pod count = %v, want 1", summary)
	}
}
