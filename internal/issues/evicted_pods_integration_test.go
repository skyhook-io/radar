package issues

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const (
	diskEvictionMessage   = "The node was low on resource: ephemeral-storage. Threshold quantity: 4013935590, available: 3812960Ki. Container app was using 2380Ki, request is 0, has larger consumption of ephemeral-storage. "
	memoryEvictionMessage = "The node was low on resource: memory. Threshold quantity: 100Mi, available: 91844Ki. Container app was using 1.5Gi, request is 512Mi, has larger consumption of memory. "
)

func evictedPod(name, node, message string, evictedAt time.Time, owner *metav1.OwnerReference) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", CreationTimestamp: metav1.NewTime(evictedAt.Add(-time.Hour))},
		Spec:       corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{
			Phase: corev1.PodFailed, Reason: "Evicted", Message: message,
			Conditions: []corev1.PodCondition{{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue, Reason: "TerminationByKubelet", LastTransitionTime: metav1.NewTime(evictedAt)}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "ContainerStatusUnknown", ExitCode: 137,
			}}}},
		},
	}
	if owner != nil {
		pod.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	return pod
}

// Evicted pods of one Deployment fold into one pod_evicted row that names every
// node that evicted them, and the node still under DiskPressure links back to
// the row. A bare evicted pod stays critical: nothing replaces it.
func TestCompose_EvictedPodsCategorizedGroupedAndLinkedToNode(t *testing.T) {
	defer k8s.ResetTestState()

	now := time.Now()
	evictedAt := now.Add(-10 * time.Minute).Truncate(time.Second)
	controller := true
	rsOwner := &metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-rs", Controller: &controller}
	replicas := int32(2)
	client := fake.NewClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
			Status:     appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 2, AvailableReplicas: 2, UpdatedReplicas: 2},
		},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: "web-rs", Namespace: "prod",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Controller: &controller}},
		}},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-a", CreationTimestamp: metav1.NewTime(now.Add(-24 * time.Hour))},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(evictedAt.Add(-time.Minute))},
			}},
		},
		evictedPod("web-1", "node-a", diskEvictionMessage, evictedAt, rsOwner),
		evictedPod("web-2", "node-a", diskEvictionMessage, evictedAt, rsOwner),
		evictedPod("web-3", "node-b", diskEvictionMessage, evictedAt, rsOwner),
		evictedPod("solo", "node-b", memoryEvictionMessage, evictedAt, nil),
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	provider := &CacheProvider{cache: k8s.GetResourceCache()}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(provider.DetectProblems(nil)) >= 5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	got := Compose(provider, Filters{Grouped: true, Limit: NoLimit})
	byName := map[string]Issue{}
	for _, i := range got {
		byName[i.Kind+"/"+i.Name+"/"+string(i.Category)] = i
	}

	web, ok := byName["Deployment/web/pod_evicted"]
	if !ok {
		t.Fatalf("issues = %+v, want one pod_evicted row for Deployment web", got)
	}
	if web.Severity != SeverityWarning || web.Count != 3 || web.CategoryGroup != issuesapi.GroupRuntime {
		t.Errorf("web row = severity %q count %d group %q, want warning / 3 / runtime", web.Severity, web.Count, web.CategoryGroup)
	}
	if web.MessageSource != issuesapi.MessageSourceRadar || !strings.Contains(web.Message, "the node was low on ephemeral-storage (eviction threshold 3.7Gi, available 3.6Gi)") {
		t.Errorf("web message = %q (source %q), want Radar's parsed eviction summary", web.Message, web.MessageSource)
	}
	if web.RawMessage != diskEvictionMessage {
		t.Errorf("web raw message = %q, want the kubelet's message", web.RawMessage)
	}
	if web.Cause != "The kubelet evicted this pod because its node ran low on ephemeral-storage." || web.Action == "" {
		t.Errorf("web cause/action = %q / %q, want the agreed disk-pressure diagnosis", web.Cause, web.Action)
	}
	if want := evictedAt.UTC(); !web.FirstSeen.Equal(want) {
		t.Errorf("web first_seen = %v, want the eviction time %v", web.FirstSeen, want)
	}
	fact := findFact(web.DiagnosticContext, factEvictedFromNode)
	if fact == nil || len(fact.Refs) != 2 || fact.Refs[0].Name != "node-a" || fact.Refs[1].Name != "node-b" ||
		fact.Message != "The kubelet evicted these pods on 2 nodes." {
		t.Fatalf("web evicted_from_node fact = %+v, want both nodes", fact)
	}

	node, ok := byName["Node/node-a/node_not_ready"]
	if !ok {
		t.Fatalf("issues = %+v, want the DiskPressure node row", got)
	}
	blast := findFact(node.DiagnosticContext, factNodeBlastRadius)
	if blast == nil || len(blast.RelatedIssues) != 1 || blast.RelatedIssues[0].Category != issuesapi.CategoryPodEvicted {
		t.Fatalf("node-a blast radius = %+v, want the web eviction row linked", blast)
	}

	solo, ok := byName["Pod/solo/pod_evicted"]
	if !ok || solo.Severity != SeverityCritical {
		t.Fatalf("solo row = %+v, want a critical pod_evicted row", solo)
	}
	if f := findFact(solo.DiagnosticContext, factEvictedFromNode); f == nil || f.Message != "The kubelet on node node-b evicted this pod." {
		t.Errorf("solo evicted_from_node fact = %+v", f)
	}
}

// A node's pressure explains only evictions of the resource it is short of.
func TestNodeBlastRadiusContext_EvictionMustMatchPressure(t *testing.T) {
	node := Issue{Kind: "Node", Name: "node-1", Category: issuesapi.CategoryNodeNotReady, Severity: SeverityCritical, Reason: "DiskPressure"}
	disk := Issue{ID: "disk", Kind: "Pod", Namespace: "prod", Name: "disk", Category: issuesapi.CategoryPodEvicted, RawMessage: diskEvictionMessage}
	memory := Issue{ID: "mem", Kind: "Pod", Namespace: "prod", Name: "mem", Category: issuesapi.CategoryPodEvicted, RawMessage: memoryEvictionMessage}
	limit := Issue{ID: "limit", Kind: "Pod", Namespace: "prod", Name: "limit", Category: issuesapi.CategoryPodEvicted,
		RawMessage: `Container app exceeded its local ephemeral storage limit "1Gi". `}
	p := &fakeProvider{podsOnNode: map[string][]Ref{"node-1": {
		{Kind: "Pod", Namespace: "prod", Name: "disk"},
		{Kind: "Pod", Namespace: "prod", Name: "mem"},
		{Kind: "Pod", Namespace: "prod", Name: "limit"},
	}}}
	out := enrichDiagnosticContext([]Issue{node}, []Issue{node, disk, memory, limit}, nil, p)
	fact := findFact(out[0].DiagnosticContext, factNodeBlastRadius)
	if fact == nil || len(fact.RelatedIssues) != 1 || fact.RelatedIssues[0].Ref.Name != "disk" {
		t.Fatalf("blast radius = %+v, want only the disk eviction", fact)
	}
}

func findFact(ctx *issuesapi.DiagnosticContext, typ string) *issuesapi.DiagnosticFact {
	if ctx == nil {
		return nil
	}
	for i := range ctx.Facts {
		if ctx.Facts[i].Type == typ {
			return &ctx.Facts[i]
		}
	}
	return nil
}
