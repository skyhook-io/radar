package issues

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCNPGScheduleDestinationProjection(t *testing.T) {
	cluster := cnpgCluster(nil, nil)
	schedule := cnpgHourly(false)
	p := cnpgScheduleProvider([]*unstructured.Unstructured{cluster}, []*unstructured.Unstructured{schedule}, nil, nil)
	p.namespaced = map[schema.GroupVersionResource]bool{cnpgClusterGVR: true, cnpgScheduledBackupGVR: true, cnpgBackupGVR: true, cnpgObjectStoreGVR: true}
	flat := Compose(p, Filters{Kinds: []string{"ScheduledBackup"}, Limit: NoLimit, AllowUnfilteredEvidence: true})
	issue := findIssue(t, flat, ReasonCNPGScheduleDestinationMissing)
	if issue.Kind != "ScheduledBackup" || issue.Name != "hourly" || len(issue.RequiredReads) != 2 || !issue.OnsetUnknown {
		t.Fatalf("unexpected issue: %+v", issue)
	}
	for _, grouped := range []bool{false, true} {
		visible := Compose(p, Filters{Kinds: []string{"ScheduledBackup"}, Grouped: grouped, Limit: NoLimit, CanReadEvidence: func(EvidenceRead) bool { return true }})
		findIssue(t, visible, ReasonCNPGScheduleDestinationMissing)
		for _, resource := range []string{"clusters", "scheduledbackups"} {
			denied := Compose(p, Filters{Grouped: grouped, Limit: NoLimit, CanReadEvidence: func(read EvidenceRead) bool { return read.Resource != resource }})
			for _, got := range denied {
				if got.Reason == ReasonCNPGScheduleDestinationMissing {
					t.Fatalf("%s denial leaked through grouped=%v", resource, grouped)
				}
			}
		}
	}
	grouped := GroupIssues(flat)
	for _, opts := range []RelatedIssueOptions{
		{CanReadRelated: func(Ref) bool { return true }},
		{CanReadClusterScoped: func(string, string) bool { return true }},
		{CanReadEvidence: func(read EvidenceRead) bool { return read.Resource != "clusters" }},
	} {
		for _, got := range RelatedIssuesFrom(flat, grouped, opts, cnpgGroup, "ScheduledBackup", "pg", "hourly") {
			if got.Reason == ReasonCNPGScheduleDestinationMissing {
				t.Fatal("cached related projection leaked required inventory")
			}
		}
	}
	visible := RelatedIssuesFrom(flat, grouped, RelatedIssueOptions{CanReadEvidence: func(EvidenceRead) bool { return true }}, cnpgGroup, "ScheduledBackup", "pg", "hourly")
	findIssue(t, visible, ReasonCNPGScheduleDestinationMissing)
	data, err := json.Marshal(issue)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "RequiredReads") || strings.Contains(string(data), "scheduledbackups") {
		t.Fatalf("internal evidence metadata serialized: %s", data)
	}

	schedule.Object["spec"].(map[string]any)["suspend"] = true
	if got := detectCNPGScheduleDestinationIssues(p, cnpgScheduledBackupGVR, []*unstructured.Unstructured{schedule}); len(got) != 0 {
		t.Fatal("suspended schedule raised a blocker")
	}
	schedule.Object["spec"].(map[string]any)["suspend"] = false
	p.listErr = map[schema.GroupVersionResource]error{cnpgClusterGVR: errors.New("unavailable")}
	if got := detectCNPGScheduleDestinationIssues(p, cnpgScheduledBackupGVR, []*unstructured.Unstructured{schedule}); len(got) != 0 {
		t.Fatal("unread cluster mistaken for missing destination")
	}
}

type cnpgObservedProvider struct {
	*fakeProvider
	pods  []*corev1.Pod
	known bool
}

func (p *cnpgObservedProvider) cnpgInstancePods(*unstructured.Unstructured) ([]*corev1.Pod, bool) {
	return p.pods, p.known
}

func TestCNPGInstanceObservationCoverage(t *testing.T) {
	cluster := cnpgCluster(nil, map[string]any{"readyInstances": int64(3), "currentPrimary": "pg-main-1"})
	instance := func(name, role string, ready corev1.ConditionStatus) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "pg", Labels: map[string]string{"cnpg.io/instanceRole": role}}, Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}}}}
	}
	p := &cnpgObservedProvider{fakeProvider: cnpgScheduleProvider([]*unstructured.Unstructured{cluster}, nil, nil, nil), known: true, pods: []*corev1.Pod{instance("pg-main-1", "primary", corev1.ConditionFalse), instance("pg-main-2", "replica", corev1.ConditionTrue), instance("pg-main-3", "replica", corev1.ConditionFalse)}}
	issue := findIssue(t, detectCNPGInstanceObservationIssues(p, cnpgClusterGVR, []*unstructured.Unstructured{cluster}), ReasonCNPGInstanceReadinessMismatch)
	if issue.Severity != SeverityCritical || issue.Message != "2 of 3 instance Pods not ready, including the primary" || len(issue.RequiredReads) != 2 {
		t.Fatalf("unexpected contradiction: %+v", issue)
	}
	p.known = false
	if got := detectCNPGInstanceObservationIssues(p, cnpgClusterGVR, []*unstructured.Unstructured{cluster}); len(got) != 0 {
		t.Fatal("unread Pod inventory judged")
	}
	p.known = true
	p.pods[0].Status.Conditions[0].Status = corev1.ConditionUnknown
	if got := detectCNPGInstanceObservationIssues(p, cnpgClusterGVR, []*unstructured.Unstructured{cluster}); len(got) != 0 {
		t.Fatal("unknown readiness judged")
	}
	p.pods[0].Status.Conditions = nil
	if got := detectCNPGInstanceObservationIssues(p, cnpgClusterGVR, []*unstructured.Unstructured{cluster}); len(got) != 0 {
		t.Fatal("unreported readiness judged")
	}
	p.pods[0].Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	cluster.SetAnnotations(map[string]string{"cnpg.io/hibernation": "on"})
	if got := detectCNPGInstanceObservationIssues(p, cnpgClusterGVR, []*unstructured.Unstructured{cluster}); len(got) != 0 {
		t.Fatal("hibernation judged as a readiness contradiction")
	}
	cluster.SetAnnotations(nil)
	cluster.Object["status"].(map[string]any)["readyInstances"] = int64(2)
	if got := detectCNPGInstanceObservationIssues(p, cnpgClusterGVR, []*unstructured.Unstructured{cluster}); len(got) != 0 {
		t.Fatal("agreeing status judged")
	}
	cluster.Object["status"].(map[string]any)["currentPrimary"] = "pg-main-2"
	findIssue(t, detectCNPGInstanceObservationIssues(p, cnpgClusterGVR, []*unstructured.Unstructured{cluster}), ReasonCNPGPrimaryLabelMismatch)
}

func TestCNPGMissingInstanceObservationDescribesInventory(t *testing.T) {
	cluster := cnpgCluster(nil, map[string]any{"readyInstances": int64(2)})
	p := &cnpgObservedProvider{fakeProvider: cnpgScheduleProvider([]*unstructured.Unstructured{cluster}, nil, nil, nil), known: true}
	issue := findIssue(t, detectCNPGInstanceObservationIssues(p, cnpgClusterGVR, []*unstructured.Unstructured{cluster}), ReasonCNPGInstanceReadinessMismatch)
	if issue.Message != "Only 0 ready instance Pods observed; CNPG status reports 2 ready" || issue.Severity != SeverityCritical {
		t.Fatalf("unexpected missing inventory message: %+v", issue)
	}
}
