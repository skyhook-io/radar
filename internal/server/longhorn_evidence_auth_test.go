package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLonghornEvidenceRequiresInstanceManagerSAR(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "allowed"}[allowed], func(t *testing.T) {
			s := newAuthServer(auth.Config{Mode: "proxy"})
			s.permCache.Set("storage-reader", nil, &auth.UserPermissions{AllowedNamespaces: []string{"storage"}})
			reviews := fakeSARServer(t, func(a authv1.ResourceAttributes) bool {
				return allowed && a.Group == "longhorn.io" && a.Resource == "instancemanagers" && a.Namespace == "storage" && a.Verb == "list"
			})
			r := requestWithUser(http.MethodGet, "/api/issues?namespace=storage", &auth.User{Username: "storage-reader"})
			access := s.issueEvidenceAccess(r)
			read := issues.EvidenceRead{Group: "longhorn.io", Resource: "instancemanagers", Namespace: "storage", Verb: "list"}
			if got := access(read); got != allowed {
				t.Fatalf("InstanceManager evidence allowed=%v, want %v", got, allowed)
			}
			if len(*reviews) != 1 || (*reviews)[0] != (authv1.ResourceAttributes{Group: "longhorn.io", Resource: "instancemanagers", Namespace: "storage", Verb: "list"}) {
				t.Fatalf("expected exact InstanceManager SAR, got %+v", *reviews)
			}
			read.Namespace = "other"
			if access(read) || len(*reviews) != 1 {
				t.Fatal("namespace gate must deny unrelated inventory before SAR")
			}
		})
	}
}

func TestDashboardLonghornEvidencePreservesPDBOnset(t *testing.T) {
	data, err := os.ReadFile("../k8s/testdata/longhorn-pdb/v1.13.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		PDB policyv1.PodDisruptionBudget `json:"pdb"`
		Pod corev1.Pod                   `json:"pod"`
		IM  unstructured.Unstructured    `json:"im"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	onset := time.Now().UTC().Add(-2 * time.Hour)
	fixture.PDB.CreationTimestamp = metav1.NewTime(onset.Add(-time.Hour))
	fixture.PDB.Status.Conditions = []metav1.Condition{{Type: policyv1.DisruptionAllowedCondition, Status: metav1.ConditionFalse, LastTransitionTime: metav1.NewTime(onset)}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(&fixture.PDB, &fixture.Pod, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fixture.PDB.Namespace}})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8s.ResetResourceCache(); _ = k8s.InitTestResourceCache(testFakeClient) })
	t.Cleanup(k8s.SetTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected}))
	gvr := schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "instancemanagers"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "InstanceManagerList"}, &fixture.IM)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "InstanceManager", Namespaced: true, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	k8s.WarmupCommonCRDs()
	if !k8s.GetDynamicResourceCache().WaitForSync(gvr, 3*time.Second) {
		t.Fatal("InstanceManager fixture did not sync")
	}
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "allowed"}[allowed], func(t *testing.T) {
			s := newAuthServer(auth.Config{Mode: "proxy"})
			s.permCache.Set("dashboard-storage-reader", nil, &auth.UserPermissions{AllowedNamespaces: []string{fixture.PDB.Namespace}})
			fakeSARServer(t, func(a authv1.ResourceAttributes) bool {
				return allowed && a.Group == gvr.Group && a.Resource == gvr.Resource && a.Namespace == fixture.PDB.Namespace && a.Verb == "list"
			})
			r := requestWithUser(http.MethodGet, "/api/dashboard?namespace="+fixture.PDB.Namespace, &auth.User{Username: "dashboard-storage-reader"})
			recorder := httptest.NewRecorder()
			s.handleDashboard(recorder, r)
			if recorder.Code != http.StatusOK {
				t.Fatalf("dashboard status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var response DashboardResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			for _, problem := range response.Problems {
				if problem.Kind != "PodDisruptionBudget" || problem.Name != fixture.PDB.Name {
					continue
				}
				if problem.OnsetUnknown || problem.Duration == "" || problem.DurationSeconds < 7190 || problem.DurationSeconds > 7210 {
					t.Fatalf("PDB-owned onset lost for evidence allowed=%v: %+v", allowed, problem)
				}
				if strings.HasPrefix(problem.Message, "Longhorn is protecting") != allowed {
					t.Fatalf("wrong dashboard evidence projection: %+v", problem)
				}
				return
			}
			t.Fatal("dashboard lost PDB warning")
		})
	}
}
