package mcp

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetResourceNodeCertificateContext(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	scenarios := []struct {
		name, state string
		severity    issues.Severity
		days        int
		age         time.Duration
	}{
		{"urgent", "reported", "critical", 6, time.Minute},
		{"renewal", "reported", "warning", 99, time.Minute},
		{"stale", "unverified", "warning", -1, 3 * time.Hour},
	}
	var objects []runtime.Object
	for _, scenario := range scenarios {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: scenario.name, UID: types.UID(scenario.name + "-uid"), CreationTimestamp: metav1.NewTime(now.Add(-48 * time.Hour))}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34.4+k3s1"}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
		event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: scenario.name + "-warning", Namespace: "default", UID: types.UID(scenario.name + "-event")}, InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: node.Name, UID: node.UID}, Type: "Warning", Reason: "CertificateExpirationWarning", LastTimestamp: metav1.NewTime(now.Add(-scenario.age)), Message: fmt.Sprintf("certificate CN=client will expire within 120 days at %s", now.Add(time.Duration(scenario.days)*24*time.Hour).Format(time.RFC3339))}
		objects = append(objects, node, event)
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8s.ResetTestState(); getPermCache().Invalidate(); timeline.ResetStore() })
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatal(err)
	}
	timeline.SetObservationStartForTest(now.Add(-24 * time.Hour))
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "fake-test"})
	ctx := withClusterAdmin(t, "node-certificate-reader")
	grantClusterRead(t, "node-certificate-reader", "/nodes")
	for _, scenario := range scenarios {
		for _, tc := range []struct {
			name, context, include string
			want                   bool
		}{{"default", "", "", true}, {"explicit", "none", "issues", true}, {"bare", "none", "", false}} {
			t.Run(scenario.name+"/"+tc.name, func(t *testing.T) {
				result, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "Node", Name: scenario.name, Context: tc.context, Include: tc.include})
				if err != nil {
					t.Fatal(err)
				}
				var response map[string]json.RawMessage
				text := extractText(t, result)
				if err := json.Unmarshal([]byte(text), &response); err != nil {
					t.Fatal(err)
				}
				report, present := response["nodeCertificateReport"]
				if present != tc.want {
					t.Fatalf("report present=%v: %s", present, text)
				}
				if tc.want {
					var got k8s.NodeCertificateReport
					if err := json.Unmarshal(report, &got); err != nil || got.State != scenario.state || got.Message == "" || got.UID != scenario.name+"-uid" {
						t.Fatalf("report lost provenance: %s", report)
					}
				}
				if tc.include == "issues" {
					var rows []issues.Issue
					if err := json.Unmarshal(response["relatedIssues"], &rows); err != nil || len(rows) != 1 || rows[0].Reason != k8s.NodeCertificateIssueReason || rows[0].Severity != scenario.severity {
						t.Fatalf("MCP warning/report parity lost: %s", text)
					}
				}
			})
		}
	}
	getPermCache().Get("node-certificate-reader", nil).SetCanI("list", "", "nodes", "", false)
	result, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "Node", Name: "urgent", Include: "issues"})
	if err != nil {
		t.Fatal(err)
	}
	var denied map[string]json.RawMessage
	if err := json.Unmarshal([]byte(extractText(t, result)), &denied); err != nil || denied["nodeCertificateReport"] != nil {
		t.Fatalf("MCP report bypassed list-nodes: %s", extractText(t, result))
	}
}
