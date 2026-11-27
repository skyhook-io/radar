package k8score

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/skyhook-io/radar/pkg/workloadrestart"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clienttesting "k8s.io/client-go/testing"
)

func TestReviewedRestartUpdateStrategy(t *testing.T) {
	for _, tc := range []struct {
		kind      string
		strategy  string
		partition int64
		wantErr   bool
	}{
		{"StatefulSet", "OnDelete", 0, true},
		{"DaemonSet", "OnDelete", 0, true},
		{"StatefulSet", "RollingUpdate", 1, true},
		{"StatefulSet", "RollingUpdate", 0, false},
		{"DaemonSet", "RollingUpdate", 0, false},
	} {
		obj := deploymentObject(podTemplateV1(), nil, nil)
		obj.SetKind(tc.kind)
		obj.Object["spec"].(map[string]any)["updateStrategy"] = map[string]any{
			"type": tc.strategy, "rollingUpdate": map[string]any{"partition": tc.partition},
		}
		client := newAppsClient(obj)
		res, err := NewWorkloadManager(client, nil).RestartReviewedWorkload(t.Context(), tc.kind, "prod", "web", workloadrestart.Preconditions{UID: obj.GetUID()})
		if tc.wantErr {
			if !errors.Is(err, ErrRestartNotApplicable) || len(patchActions(client)) != 0 {
				t.Errorf("%+v: patches = %v, error = %v", tc, patchActions(client), err)
			}
		} else if err != nil || !res.Changed || res.RestartedAt == "" {
			t.Errorf("%+v: result = %+v, error = %v", tc, res, err)
		}
	}
}

func TestReviewedRestartRechecksAfterVersionConflict(t *testing.T) {
	for _, tc := range []struct {
		name, version                        string
		nextUID                              types.UID
		paused, terminating, keepConflicting bool
		wantErr                              error
		wantPatches                          int
	}{
		{name: "status churn", nextUID: "dep-uid", wantPatches: 2},
		{name: "replacement", nextUID: "new", wantErr: workloadrestart.ErrChanged, wantPatches: 1},
		{name: "concurrent deletion", nextUID: "dep-uid", terminating: true, wantErr: ErrWorkloadTerminating, wantPatches: 1},
		{name: "concurrent pause", nextUID: "dep-uid", paused: true, wantErr: ErrRestartPaused, wantPatches: 1},
		{name: "strict version", version: "42", nextUID: "dep-uid", wantErr: workloadrestart.ErrChanged, wantPatches: 1},
		{name: "retry limit", nextUID: "dep-uid", keepConflicting: true, wantErr: workloadrestart.ErrChanged, wantPatches: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := deploymentObject(podTemplateV1(), nil, nil)
			client := newAppsClient(obj)
			patches := 0
			client.PrependReactor("patch", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
				patches++
				if patches > 1 && !tc.keepConflicting {
					return false, nil, nil
				}
				next := obj.DeepCopy()
				next.SetUID(tc.nextUID)
				if tc.terminating {
					now := metav1.Now()
					next.SetDeletionTimestamp(&now)
				}
				next.SetResourceVersion(strconv.Itoa(42 + patches))
				next.Object["spec"].(map[string]any)["paused"] = tc.paused
				if err := client.Tracker().Update(deploymentsGVR, next, "prod"); err != nil {
					t.Fatal(err)
				}
				return true, nil, apierrors.NewConflict(a.GetResource().GroupResource(), "web", errors.New("concurrent write"))
			})
			result, err := NewWorkloadManager(client, nil).RestartReviewedWorkload(t.Context(), "Deployment", "prod", "web", workloadrestart.Preconditions{UID: obj.GetUID(), ResourceVersion: tc.version})
			if !errors.Is(err, tc.wantErr) || patches != tc.wantPatches {
				t.Fatalf("patches = %d, result = %+v, err = %v", patches, result, err)
			}
			if tc.wantErr == nil {
				var patch unstructured.Unstructured
				if err := json.Unmarshal(patchActions(client)[1].GetPatch(), &patch.Object); err != nil {
					t.Fatal(err)
				}
				if !result.Changed || patch.GetResourceVersion() != "43" || patch.GetUID() != obj.GetUID() {
					t.Errorf("retry = %v, result = %+v", patch.Object, result)
				}
			}
		})
	}
}
