package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestDrainOptionsFromRequestDefaults(t *testing.T) {
	no := false
	yes := true
	grace := int64(5)

	tests := []struct {
		name            string
		req             DrainRequest
		emptyDirDefault bool
		wantEmptyDir    bool
		wantForce       bool
		wantTimeout     time.Duration
	}{
		{name: "drain keeps its historical emptyDir default", req: DrainRequest{}, emptyDirDefault: true, wantEmptyDir: true, wantTimeout: 60 * time.Second},
		{name: "plan never includes emptyDir data unless asked", req: DrainRequest{}, emptyDirDefault: false, wantEmptyDir: false, wantTimeout: 60 * time.Second},
		{name: "explicit false overrides the drain default", req: DrainRequest{DeleteEmptyDirData: &no}, emptyDirDefault: true, wantEmptyDir: false, wantTimeout: 60 * time.Second},
		{name: "explicit true overrides the plan default", req: DrainRequest{DeleteEmptyDirData: &yes, Force: true, Timeout: 120}, emptyDirDefault: false, wantEmptyDir: true, wantForce: true, wantTimeout: 120 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := drainOptionsFromRequest(tt.req, tt.emptyDirDefault)
			if !got.IgnoreDaemonSets {
				t.Fatalf("DaemonSets must always be ignored")
			}
			if got.DeleteEmptyDirData != tt.wantEmptyDir || got.Force != tt.wantForce || got.Timeout != tt.wantTimeout {
				t.Fatalf("got %+v", got)
			}
		})
	}

	got := drainOptionsFromRequest(DrainRequest{GracePeriodSeconds: &grace}, false)
	if got.GracePeriodSeconds == nil || *got.GracePeriodSeconds != 5 {
		t.Fatalf("grace period must pass through, got %+v", got.GracePeriodSeconds)
	}
}

func drainPlanTestClient() *fake.Clientset {
	yes := true
	return fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web", Controller: &yes}}},
			Spec:       corev1.PodSpec{NodeName: "worker-1"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)
}

func TestWriteDrainPlanRespondsWithPlanAndNeverMutates(t *testing.T) {
	client := drainPlanTestClient()
	s := &Server{}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/nodes/worker-1/drain-plan", nil)

	s.writeDrainPlan(rr, req, client, "worker-1", k8s.DrainOptions{IgnoreDaemonSets: true})

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	var plan k8s.DrainPlan
	if err := json.Unmarshal(rr.Body.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if !plan.Estimate || plan.Node != "worker-1" || plan.Summary.Evict != 1 || len(plan.Pods) != 1 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() != "get" && a.GetVerb() != "list" {
			t.Fatalf("plan endpoint must not mutate the cluster, saw %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
}

func TestWriteDrainPlanStatusMapping(t *testing.T) {
	t.Run("unknown node is 404", func(t *testing.T) {
		s := &Server{}
		rr := httptest.NewRecorder()
		s.writeDrainPlan(rr, httptest.NewRequest(http.MethodPost, "/", nil), drainPlanTestClient(), "ghost", k8s.DrainOptions{})
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
		}
	})
	t.Run("forbidden pod list is 403", func(t *testing.T) {
		client := drainPlanTestClient()
		client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", nil)
		})
		s := &Server{}
		rr := httptest.NewRecorder()
		s.writeDrainPlan(rr, httptest.NewRequest(http.MethodPost, "/", nil), client, "worker-1", k8s.DrainOptions{})
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
		}
	})
	t.Run("forbidden PDB list degrades to an unevaluated plan, not an error", func(t *testing.T) {
		client := drainPlanTestClient()
		client.PrependReactor("list", "poddisruptionbudgets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "policy", Resource: "poddisruptionbudgets"}, "", nil)
		})
		s := &Server{}
		rr := httptest.NewRecorder()
		s.writeDrainPlan(rr, httptest.NewRequest(http.MethodPost, "/", nil), client, "worker-1", k8s.DrainOptions{})
		var plan k8s.DrainPlan
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &plan) != nil || plan.PDBsEvaluated || plan.PDBError == "" {
			t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
		}
	})
}

func TestDrainPlanOptionsFromHTTP(t *testing.T) {
	t.Run("empty body applies the plan defaults", func(t *testing.T) {
		opts, err := drainPlanOptionsFromHTTP(httptest.NewRequest(http.MethodPost, "/", nil))
		if err != nil || opts.DeleteEmptyDirData || opts.Force || !opts.IgnoreDaemonSets {
			t.Fatalf("got %+v, %v", opts, err)
		}
	})
	t.Run("explicit options are honoured", func(t *testing.T) {
		body := strings.NewReader(`{"deleteEmptyDirData":true,"force":true,"gracePeriodSeconds":7}`)
		opts, err := drainPlanOptionsFromHTTP(httptest.NewRequest(http.MethodPost, "/", body))
		if err != nil || !opts.DeleteEmptyDirData || !opts.Force || opts.GracePeriodSeconds == nil || *opts.GracePeriodSeconds != 7 {
			t.Fatalf("got %+v, %v", opts, err)
		}
	})
	t.Run("malformed body is rejected", func(t *testing.T) {
		if _, err := drainPlanOptionsFromHTTP(httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{"))); err == nil {
			t.Fatalf("expected a decode error")
		}
	})
}
