package k8s

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func certificateTestNode(now time.Time) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "control-plane", UID: "node-current", CreationTimestamp: metav1.NewTime(now.Add(-365 * 24 * time.Hour))},
		Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34.4+rke2r1"}}}
}

func certificateTestEvent(node *corev1.Node, reported, expires time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "certificate-warning", Namespace: "default", UID: "event-warning"},
		InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: node.Name, UID: types.UID(node.Name)},
		Type:           corev1.EventTypeWarning, Reason: nodeCertificateWarningReason,
		Message:       fmt.Sprintf("Node certificates require attention - restart rke2 on this node to trigger automatic rotation: etcd/client.crt: certificate CN=etcd-client will expire within 120 days at %s", expires.UTC().Format(time.RFC3339)),
		LastTimestamp: metav1.NewTime(reported),
	}
}

func completeCertificateHistory(now time.Time) nodeCertificateHistory {
	return nodeCertificateHistory{incompleteBefore: now.Add(-24 * time.Hour)}
}

func TestNodeCertificateUrgencyAndObservation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name         string
		remaining    time.Duration
		reportAge    time.Duration
		unavailable  bool
		wantSeverity string
		wantState    string
	}{
		{"renewal-window", 99 * 24 * time.Hour, time.Minute, false, "warning", "reported"},
		{"thirty-day-boundary", 30 * 24 * time.Hour, time.Minute, false, "warning", "reported"},
		{"warning", 29 * 24 * time.Hour, time.Minute, false, "warning", "reported"},
		{"seven-day-boundary", 7 * 24 * time.Hour, time.Minute, false, "warning", "reported"},
		{"critical", 6 * 24 * time.Hour, time.Minute, false, "critical", "reported"},
		{"expired", -time.Hour, time.Minute, false, "critical", "reported"},
		{"stale", -time.Hour, 3 * time.Hour, false, "warning", "unverified"},
		{"collection-unavailable", 6 * 24 * time.Hour, time.Minute, true, "warning", "unverified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := certificateTestNode(now)
			event := certificateTestEvent(node, now.Add(-tc.reportAge), now.Add(tc.remaining))
			history := completeCertificateHistory(now)
			history.unavailable = tc.unavailable
			got := assessNodeCertificates([]*corev1.Node{node}, []*corev1.Event{event}, history, !tc.unavailable, now)
			report := got.reports[node.Name]
			if report.State != tc.wantState || report.UID != string(node.UID) || report.EventID != string(event.UID) || report.Message != event.Message {
				t.Fatalf("wrong report: %+v", report)
			}
			if len(got.detections) != 1 || got.detections[0].Severity != tc.wantSeverity || got.detections[0].Name != node.Name || got.detections[0].Reason != NodeCertificateIssueReason {
				t.Fatalf("wrong issue: %+v", got.detections)
			}
			if !got.detections[0].OnsetUnknown || got.detections[0].RawMessage != event.Message || !strings.Contains(got.detections[0].Message, "last reported") {
				t.Fatalf("report provenance lost: %+v", got.detections[0])
			}
			if tc.wantState == "unverified" && (!strings.Contains(got.detections[0].Message, "needs verification") || !strings.Contains(got.detections[0].Cause, "current certificate state is unverified")) {
				t.Fatalf("unverified Issue asserted a current certificate failure: %+v", got.detections[0])
			}
		})
	}
}

func TestNodeCertificateReportLifecycle(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	node := certificateTestNode(now)
	warning := certificateTestEvent(node, now.Add(-5*time.Minute), now.Add(6*24*time.Hour))
	history := completeCertificateHistory(now)
	history.rows = []timeline.TimelineEvent{timeline.NewK8sEventTimelineEvent(warning, nil)}
	got := assessNodeCertificates([]*corev1.Node{node}, nil, history, true, now)
	if len(got.detections) != 1 {
		t.Fatalf("native Event deletion lost retained warning: %+v", got)
	}
	got = assessNodeCertificates([]*corev1.Node{node}, nil, history, true, now.Add(3*time.Hour))
	if len(got.detections) != 1 || got.detections[0].Severity != "warning" || got.reports[node.Name].State != "unverified" {
		t.Fatalf("silence hid the unresolved warning or asserted current expiry: %+v", got)
	}

	ca := *warning
	ca.UID, ca.Reason, ca.LastTimestamp = "ca-warning", "CACertificateExpirationWarning", metav1.NewTime(now.Add(-4*time.Minute))
	if got := assessNodeCertificates([]*corev1.Node{node}, []*corev1.Event{&ca}, history, true, now); len(got.detections) != 1 {
		t.Fatal("CA-only report invented leaf renewal")
	}
	monitorOK := *warning
	monitorOK.UID, monitorOK.Type, monitorOK.Reason = "event-ok", corev1.EventTypeNormal, nodeCertificateOKReason
	monitorOK.LastTimestamp, monitorOK.Message = metav1.NewTime(now.Add(-3*time.Minute)), "Node and Certificate Authority certificates managed by rke2 are OK"
	history.rows = append(history.rows, timeline.NewK8sEventTimelineEvent(&monitorOK, nil))
	got = assessNodeCertificates([]*corev1.Node{node}, []*corev1.Event{warning}, history, true, now)
	if len(got.detections) != 0 || got.reports[node.Name].State != "ok" {
		t.Fatalf("later OK failed to supersede warning: %+v", got)
	}
	staleOK := assessNodeCertificates([]*corev1.Node{node}, nil, history, true, now.Add(3*time.Hour))
	if len(staleOK.detections) != 0 || staleOK.reports[node.Name].State != "unverified" || staleOK.reports[node.Name].Reason != nodeCertificateOKReason {
		t.Fatalf("old OK claimed current health or revived a warning: %+v", staleOK)
	}
	gappedHistory := history
	gappedHistory.incompleteBefore = now
	gappedOK := assessNodeCertificates([]*corev1.Node{node}, nil, gappedHistory, true, now)
	if len(gappedOK.detections) != 0 || gappedOK.reports[node.Name].State != "unverified" || !gappedOK.reports[node.Name].HistoryIncomplete {
		t.Fatalf("unobserved OK gap claimed current health: %+v", gappedOK)
	}
	warning.LastTimestamp = metav1.NewTime(now.Add(-time.Minute))
	if got := assessNodeCertificates([]*corev1.Node{node}, []*corev1.Event{warning}, history, true, now); len(got.detections) != 1 {
		t.Fatal("new warning after OK did not reopen")
	}

	old := certificateTestEvent(node, now.Add(-time.Minute), now.Add(99*24*time.Hour))
	history.rows = []timeline.TimelineEvent{timeline.NewK8sEventTimelineEvent(old, nil)}
	got = assessNodeCertificates([]*corev1.Node{node}, nil, history, true, now.Add(98*24*time.Hour))
	if len(got.detections) != 1 || got.detections[0].Severity != "warning" || !strings.Contains(got.detections[0].Message, "needs verification") || got.reports[node.Name].State != "unverified" {
		t.Fatalf("old report disappeared or aged into a current expiry claim: %+v", got)
	}
}

func TestNodeCertificateLiveReportDoesNotDependOnHistoricalCoverage(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	node := certificateTestNode(now)
	event := certificateTestEvent(node, now.Add(-15*time.Minute), now.Add(6*24*time.Hour))
	for _, history := range []nodeCertificateHistory{{incompleteBefore: now}, {unavailable: true}} {
		got := assessNodeCertificates([]*corev1.Node{node}, []*corev1.Event{event}, history, true, now)
		if len(got.detections) != 1 || got.reports[node.Name].HistoryIncomplete {
			t.Fatalf("complete live report was hidden by an unrelated history gap: %+v", got)
		}
		history.unavailable = true
		got = assessNodeCertificates([]*corev1.Node{node}, []*corev1.Event{event}, history, false, now)
		if len(got.detections) != 1 || got.detections[0].Severity != "warning" || got.reports[node.Name].State != "unverified" {
			t.Fatalf("incomplete live source lost the warning or claimed current urgency: %+v", got)
		}
	}
}

func TestNodeCertificateCollectionNeedsDefaultNamespaceOnly(t *testing.T) {
	for _, namespace := range []string{"default", "tenant"} {
		t.Run(namespace, func(t *testing.T) {
			ResetTestState()
			t.Cleanup(ResetTestState)
			timeline.ResetStore()
			t.Cleanup(timeline.ResetStore)
			if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			timeline.SetObservationStartForTest(now)
			node := certificateTestNode(now)
			event := certificateTestEvent(node, now.Add(-15*time.Minute), now.Add(6*24*time.Hour))
			if err := InitScopedTestResourceCache(fake.NewClientset(node, event), map[string]k8score.ResourceScope{
				k8score.Nodes: {Enabled: true}, k8score.Events: {Enabled: true, Namespace: namespace},
			}); err != nil {
				t.Fatal(err)
			}
			row := timeline.NewK8sEventTimelineEvent(event, nil)
			row.ClusterContext = ActiveClusterContext()
			if err := timeline.GetStore().AppendBatch(t.Context(), []timeline.TimelineEvent{row}); err != nil {
				t.Fatal(err)
			}
			got := nodeCertificateReports(GetResourceCache(), NewDetectScope(), now)
			if namespace == "default" {
				if len(got.detections) != 1 || got.reports[node.Name].State != "reported" {
					t.Fatalf("default-only Events hid a fresh live report: %+v", got)
				}
			} else if len(got.detections) != 1 || got.detections[0].Severity != "warning" || got.reports[node.Name].State != "unverified" {
				t.Fatalf("an uncovered default namespace lost the warning or claimed current urgency: %+v", got)
			}
		})
	}
}

func TestNodeCertificateReportIdentityAndGaps(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name       string
		mutate     func(*corev1.Node, *corev1.Event)
		wantReport bool
	}{
		{"native-ref", func(*corev1.Node, *corev1.Event) {}, true},
		{"real-uid", func(n *corev1.Node, e *corev1.Event) { e.InvolvedObject.UID = n.UID }, true},
		{"conflicting-uid", func(_ *corev1.Node, e *corev1.Event) { e.InvolvedObject.UID = "old-node" }, false},
		{"other-group", func(_ *corev1.Node, e *corev1.Event) { e.InvolvedObject.APIVersion = "example.com/v1" }, false},
		{"other-kind", func(_ *corev1.Node, e *corev1.Event) { e.InvolvedObject.Kind = "Pod" }, false},
		{"namespaced-node-ref", func(_ *corev1.Node, e *corev1.Event) { e.Namespace, e.InvolvedObject.Namespace = "tenant", "tenant" }, false},
		{"future", func(_ *corev1.Node, e *corev1.Event) { e.LastTimestamp = metav1.NewTime(now.Add(time.Hour)) }, false},
		{"recreated-node", func(n *corev1.Node, _ *corev1.Event) { n.CreationTimestamp = metav1.NewTime(now) }, false},
		{"other-distro", func(n *corev1.Node, _ *corev1.Event) { n.Status.NodeInfo.KubeletVersion = "v1.34.4-gke.1" }, false},
		{"k3s", func(n *corev1.Node, _ *corev1.Event) { n.Status.NodeInfo.KubeletVersion = "v1.34.4+k3s1" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := certificateTestNode(now)
			event := certificateTestEvent(node, now.Add(-time.Minute), now.Add(time.Hour))
			tc.mutate(node, event)
			got := assessNodeCertificates([]*corev1.Node{node}, []*corev1.Event{event}, completeCertificateHistory(now), true, now)
			if (len(got.reports) > 0) != tc.wantReport {
				t.Fatalf("wrong identity result: %+v", got)
			}
		})
	}
	node := certificateTestNode(now)
	event := certificateTestEvent(node, now.Add(-time.Minute), now.Add(time.Hour))
	history := completeCertificateHistory(now)
	history.incompleteBefore = now.Add(-30 * time.Second)
	history.rows = []timeline.TimelineEvent{timeline.NewK8sEventTimelineEvent(event, nil)}
	got := assessNodeCertificates([]*corev1.Node{node}, nil, history, true, now)
	if len(got.detections) != 1 || got.detections[0].Severity != "warning" || got.reports[node.Name].State != "unverified" || !got.reports[node.Name].HistoryIncomplete {
		t.Fatalf("history gap hid the unresolved warning or asserted current failure: %+v", got)
	}
}

func TestNodeCertificateMessageParsing(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	node := certificateTestNode(now)
	for _, tc := range []struct {
		name, message, severity string
		unknown                 bool
	}{
		{"malformed", "certificate CN=client expired at not-a-date", "warning", true},
		{"not-yet-valid", "certificate CN=client is not valid before " + now.Add(time.Hour).Format(time.RFC3339), "critical", false},
		{"clock-disagreement", "certificate CN=client is not valid before " + now.Add(-time.Hour).Format(time.RFC3339), "warning", false},
		{"expired-clock-disagreement", "certificate CN=client expired at " + now.Add(99*24*time.Hour).Format(time.RFC3339), "warning", false},
		{"expired-clock-disagreement-near", "certificate CN=client expired at " + now.Add(5*24*time.Hour).Format(time.RFC3339), "critical", false},
		{"clock-and-expiry", "certificate CN=a is not valid before " + now.Add(-time.Hour).Format(time.RFC3339) + ", certificate CN=b expired at " + now.Add(-time.Hour).Format(time.RFC3339), "critical", false},
		{"mixed", "[certificate CN=a will expire within 120 days at " + now.Add(99*24*time.Hour).Format(time.RFC3339) + ", certificate CN=b expired at " + now.Add(-time.Hour).Format(time.RFC3339) + "]", "critical", false},
		{"newlines", "certificate CN=b expired at " + now.Add(-time.Hour).Format(time.RFC3339) + "\ncertificate CN=a will expire within 120 days at " + now.Add(99*24*time.Hour).Format(time.RFC3339), "critical", false},
		{"ca-in-node-bundle", "etcd/client.crt: certificate CN=rke2-etcd-ca@123 will expire within 120 days at " + now.Add(6*24*time.Hour).Format(time.RFC3339), "critical", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := certificateTestEvent(node, now.Add(-time.Minute), now.Add(time.Hour))
			event.Message = tc.message
			got := assessNodeCertificates([]*corev1.Node{node}, []*corev1.Event{event}, completeCertificateHistory(now), true, now)
			if got.reports[node.Name].ValidityUnknown != tc.unknown {
				t.Fatalf("wrong parse coverage: %+v", got)
			}
			if len(got.detections) != 1 || got.detections[0].Severity != tc.severity {
				t.Fatalf("wrong severity: %+v", got)
			}
			if strings.HasPrefix(tc.name, "expired-clock-disagreement") && !strings.Contains(got.detections[0].Message, "future on Radar's clock") {
				t.Fatalf("clock disagreement was hidden by urgency: %+v", got.detections[0])
			}
			if tc.name == "ca-in-node-bundle" && !strings.Contains(got.detections[0].Action, "CA certificates require separate CA rotation") {
				t.Fatalf("CA bundle report promised restart-based CA renewal: %+v", got.detections[0])
			}
		})
	}
}

func TestNodeCertificateSeriesOrderingAndStableIdentity(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	node := certificateTestNode(now)
	warning := certificateTestEvent(node, now.Add(-4*time.Hour), now.Add(6*24*time.Hour))
	warning.Series = &corev1.EventSeries{Count: 8, LastObservedTime: metav1.NewMicroTime(now.Add(-time.Minute))}
	ok := warning.DeepCopy()
	ok.UID, ok.Type, ok.Reason, ok.Series = "later-ok", corev1.EventTypeNormal, nodeCertificateOKReason, nil
	ok.LastTimestamp = metav1.NewTime(now.Add(-2 * time.Minute))
	history := completeCertificateHistory(now)
	history.rows = []timeline.TimelineEvent{timeline.NewK8sEventTimelineEvent(ok, nil), timeline.NewK8sEventTimelineEvent(warning, nil)}
	got := assessNodeCertificates([]*corev1.Node{node}, []*corev1.Event{ok, warning}, history, true, now)
	if len(got.detections) != 1 || got.reports[node.Name].LastReportedAt != warning.Series.LastObservedTime.Time {
		t.Fatalf("series repeat was not the newest observation: %+v", got)
	}
	fingerprint := got.detections[0].Fingerprint
	warning.UID = "replacement-event"
	warning.Message += "\ncertificate CN=other will expire within 120 days at " + now.Add(99*24*time.Hour).Format(time.RFC3339)
	got = assessNodeCertificates([]*corev1.Node{node}, []*corev1.Event{warning}, completeCertificateHistory(now), true, now)
	if len(got.detections) != 1 || got.detections[0].Fingerprint != fingerprint || got.detections[0].Severity != "critical" {
		t.Fatalf("message/count/Event identity changed the Issue identity: %+v", got)
	}
}

func TestNodeCertificateHistoryUsesSharedStore(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatal(err)
	}
	previous := SetTestContextName("certificate-cluster")
	t.Cleanup(func() { SetTestContextName(previous) })
	now := time.Now().UTC().Truncate(time.Second)
	timeline.SetObservationStartForTest(now.Add(-24 * time.Hour))
	node := certificateTestNode(now)
	event := certificateTestEvent(node, now.Add(-time.Minute), now.Add(time.Hour))
	row := timeline.NewK8sEventTimelineEvent(event, nil)
	row.ClusterContext = ActiveClusterContext()
	other := row
	other.ID = "other-cluster-event"
	other.ClusterContext = "other-cluster"
	noise := row
	noise.ID, noise.Reason, noise.EventType = "normal-node-noise", "NodeReady", timeline.EventTypeNormal
	if err := timeline.GetStore().AppendBatch(t.Context(), []timeline.TimelineEvent{row, other, noise}); err != nil {
		t.Fatal(err)
	}
	history := readNodeCertificateHistory([]*corev1.Node{node})
	if history.unavailable || len(history.rows) != 1 || history.rows[0].ID != row.ID {
		t.Fatalf("wrong history scope: %+v", history)
	}
	got := assessNodeCertificates([]*corev1.Node{node}, nil, history, true, now)
	if len(got.detections) != 1 {
		t.Fatalf("retained history did not power detection: %+v", got)
	}
	// A later recovery can be missing even after the store starts reading again.
	timeline.RecordDrop("K8sEvent:Node", "", node.Name, timeline.DropReasonStoreFailed, "create", ActiveClusterContext())
	t.Cleanup(func() { timeline.GetMetrics().Reset() })
	history = readNodeCertificateHistory([]*corev1.Node{node})
	got = assessNodeCertificates([]*corev1.Node{node}, nil, history, true, time.Now())
	if len(got.detections) != 1 || got.detections[0].Severity != "warning" || got.reports[node.Name].State != "unverified" || !got.reports[node.Name].HistoryIncomplete {
		t.Fatalf("known lost recovery hid uncertainty or asserted current urgency: %+v", got)
	}
}
