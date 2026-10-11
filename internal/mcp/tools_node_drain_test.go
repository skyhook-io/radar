package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestNodeDrainIncludeReadOnlyBoundedAndCallerScoped(t *testing.T) {
	for _, mode := range []string{"complete", "complete-small", "pdb-denied", "pods-denied"} {
		t.Run(mode, func(t *testing.T) {
			username := "node-drain-" + mode
			pods := corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}}
			pendingCount := 111
			if mode == "complete-small" {
				pendingCount = 2
			}
			for i := 0; i < pendingCount; i++ {
				pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pending-%03d", i), Namespace: "shop"}, Spec: corev1.PodSpec{NodeName: "worker"}, Status: corev1.PodStatus{Phase: corev1.PodPending}}
				if i == 0 {
					stamp := metav1.NewTime(time.Now().Add(-time.Minute))
					pod.DeletionTimestamp = &stamp
				}
				pods.Items = append(pods.Items, pod)
			}
			pods.Items = append(pods.Items, corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "z-active", Namespace: "shop", Labels: map[string]string{"app": "web"}}, Spec: corev1.PodSpec{NodeName: "worker"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}})
			pods.Items = append(pods.Items, corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a-completed", Namespace: "shop"}, Spec: corev1.PodSpec{NodeName: "worker"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodGet {
					t.Errorf("drain estimate mutated cluster: %s %s", r.Method, r.URL)
					w.WriteHeader(500)
					return
				}
				if r.Header.Get("Impersonate-User") != username {
					t.Errorf("request did not use caller: %v", r.Header.Values("Impersonate-User"))
				}
				forbidden := func() {
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusFailure, Reason: metav1.StatusReasonForbidden, Message: "read forbidden", Code: 403})
				}
				switch r.URL.Path {
				case "/api/v1/nodes/worker":
					_ = json.NewEncoder(w).Encode(corev1.Node{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"}, ObjectMeta: metav1.ObjectMeta{Name: "worker"}})
				case "/api/v1/pods":
					if mode == "pods-denied" {
						forbidden()
						return
					}
					if r.URL.Query().Get("fieldSelector") != "spec.nodeName=worker" {
						t.Errorf("unscoped pod read: %s", r.URL)
					}
					_ = json.NewEncoder(w).Encode(pods)
				case "/apis/policy/v1/namespaces/shop/poddisruptionbudgets":
					if mode == "pdb-denied" {
						forbidden()
						return
					}
					_ = json.NewEncoder(w).Encode(policyv1.PodDisruptionBudgetList{TypeMeta: metav1.TypeMeta{APIVersion: "policy/v1", Kind: "PodDisruptionBudgetList"}, Items: []policyv1.PodDisruptionBudget{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}}}})
				default:
					t.Errorf("unexpected read %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			config := &rest.Config{Host: server.URL}
			client, err := kubernetes.NewForConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			previousClient := k8s.SetTestClient(client)
			defer k8s.SetTestClient(previousClient)
			previousConfig := k8s.SetTestConfig(config)
			defer k8s.SetTestConfig(previousConfig)
			ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: username})
			result := map[string]any{}
			attachResourceExtras(ctx, nil, result, map[string]bool{"drain-plan": true}, "Node", "", "", "worker")
			if _, unknown := result["includeError"]; unknown {
				t.Fatalf("drain-plan not registered: %+v", result)
			}
			if mode == "pods-denied" {
				if result["drainPlan"] != nil || !strings.Contains(fmt.Sprint(result["drainPlanError"]), "forbidden") {
					t.Fatalf("failed read fabricated plan: %+v", result)
				}
				return
			}
			plan, ok := result["drainPlan"].(*k8score.DrainPlan)
			if !ok {
				t.Fatalf("missing plan: %+v", result)
			}
			wantRows := 100
			if mode == "complete-small" {
				wantRows = pendingCount + 2
			}
			if !plan.Estimate || len(plan.Pods) != wantRows || result["drainPlanTotalPods"] != pendingCount+2 || result["drainPlanPodsTruncated"] != (mode != "complete-small") {
				t.Fatalf("bad bounds: %+v %+v", plan, result)
			}
			if !plan.Options.IgnoreDaemonSets || !plan.Options.DeleteEmptyDirData || !plan.Options.Force {
				t.Fatalf("removal estimate options drifted: %+v", plan.Options)
			}
			pdbNamed, terminationSeen := false, false
			for _, pod := range plan.Pods {
				pdbNamed = pdbNamed || pod.PDB == "shop/web"
				terminationSeen = terminationSeen || pod.Terminating
				if mode != "complete-small" && (pod.Terminating || pod.Outcome == k8score.DrainOutcomeSkip) {
					t.Fatal("low-priority pod displaced a current blocker or remaining pod")
				}
			}
			if mode == "complete" || mode == "complete-small" {
				if !plan.PDBsEvaluated || plan.Summary.MayBlock != 1 || plan.Summary.Evict != pendingCount || plan.Summary.Skip != 1 || !pdbNamed {
					t.Fatalf("lost full counts/PDB match: %+v", plan)
				}
				if mode == "complete" && plan.Pods[0].PDB != "shop/web" {
					t.Fatal("current blocker was not prioritized")
				}
			} else if plan.PDBsEvaluated || plan.PDBError == "" || plan.Pods[0].PDBChecked {
				t.Fatalf("partial budget read hidden: %+v", plan)
			}
			if terminationSeen != (mode == "complete-small") {
				t.Fatal("observed pod termination missing or prioritized over current pods")
			}
		})
	}
}

func TestNodeDrainIncludeFailsClosedWithoutCallerClient(t *testing.T) {
	previousConfig := k8s.SetTestConfig(nil)
	defer k8s.SetTestConfig(previousConfig)
	previousClient := k8s.SetTestClient(&kubernetes.Clientset{})
	defer k8s.SetTestClient(previousClient)
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "drain-impersonation-failure"})
	result := map[string]any{}
	attachResourceExtras(ctx, nil, result, map[string]bool{"drain-plan": true}, "Node", "", "", "worker")
	if result["drainPlan"] != nil || result["drainPlanError"] != "cluster client unavailable" {
		t.Fatalf("did not fail closed: %+v", result)
	}
	for _, kind := range []string{"Pod", "Node"} {
		result := map[string]any{}
		attachResourceExtras(context.Background(), nil, result, map[string]bool{"drain-plan": true}, kind, "example.com", "", "worker")
		if result["drainPlanError"] != "drain-plan is available only for core Nodes" {
			t.Fatalf("non-core resource accepted: %+v", result)
		}
	}
}
