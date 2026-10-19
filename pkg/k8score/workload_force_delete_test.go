package k8score

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

var configMapGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

func mkConfigMap(finalizers []string, terminating bool) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "held", "namespace": "ns"},
	}}
	obj.SetFinalizers(finalizers)
	obj.SetUID("uid-a")
	if terminating {
		ts := metav1.Now()
		obj.SetDeletionTimestamp(&ts)
	}
	return obj
}

// newDeleteFixture returns a manager over a fake client whose delete behaves
// like the API server: an object with finalizers gets a deletionTimestamp and
// stays, and an object without them is removed. The fake tracker on its own
// removes everything immediately, which would hide the terminating state.
func newDeleteFixture(t *testing.T, obj *unstructured.Unstructured) (*WorkloadManager, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{configMapGVR: "ConfigMapList"},
		obj,
	)
	dyn.PrependReactor("delete", "configmaps", func(a clienttesting.Action) (bool, runtime.Object, error) {
		del := a.(clienttesting.DeleteAction)
		tracker := dyn.Tracker()
		current, err := tracker.Get(configMapGVR, del.GetNamespace(), del.GetName())
		if err != nil {
			return true, nil, err
		}
		u := current.(*unstructured.Unstructured)
		if len(u.GetFinalizers()) == 0 {
			return true, nil, tracker.Delete(configMapGVR, del.GetNamespace(), del.GetName())
		}
		if u.GetDeletionTimestamp() == nil {
			ts := metav1.Now()
			u.SetDeletionTimestamp(&ts)
			return true, nil, tracker.Update(configMapGVR, u, del.GetNamespace())
		}
		return true, nil, nil
	})
	// The fake tracker reports a failed JSON patch test op as a plain error;
	// the API server answers 422 Invalid and writes nothing. Check the test
	// ops this code sends (uid, finalizers) the way the server would.
	dyn.PrependReactor("patch", "configmaps", func(a clienttesting.Action) (bool, runtime.Object, error) {
		p := a.(clienttesting.PatchAction)
		if p.GetPatchType() != types.JSONPatchType {
			return false, nil, nil
		}
		var ops []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(p.GetPatch(), &ops); err != nil {
			return true, nil, err
		}
		current, err := dyn.Tracker().Get(configMapGVR, p.GetNamespace(), p.GetName())
		if err != nil {
			return true, nil, err
		}
		u := current.(*unstructured.Unstructured)
		for _, op := range ops {
			if op.Op != "test" {
				continue
			}
			var have any = string(u.GetUID())
			if op.Path == "/metadata/finalizers" {
				have = u.GetFinalizers()
			}
			want, _ := json.Marshal(have)
			if string(want) != string(op.Value) {
				return true, nil, apierrors.NewInvalid(schema.GroupKind{Kind: "ConfigMap"}, p.GetName(), nil)
			}
		}
		return false, nil, nil
	})
	return NewWorkloadManager(dyn, stubDiscovery(t, "ConfigMap", configMapGVR)), dyn
}

// gone is a result that observed no remaining object: no deletion in
// progress and no failed read.
func gone(res *DeleteResourceResult) bool {
	return res.DeletionTimestamp == nil && len(res.PendingFinalizers) == 0 && res.ObservationError == ""
}

func deleteOpts(force bool) DeleteResourceOptions {
	return DeleteResourceOptions{Kind: "ConfigMap", Namespace: "ns", Name: "held", Force: force}
}

// stripOpts is a force delete whose caller confirmed removing exactly these
// finalizers.
func stripOpts(finalizers ...string) DeleteResourceOptions {
	o := deleteOpts(true)
	o.RemoveFinalizers = finalizers
	o.ExpectedUID = "uid-a"
	return o
}

func patchActions(dyn *dynamicfake.FakeDynamicClient) []clienttesting.PatchAction {
	var out []clienttesting.PatchAction
	for _, a := range dyn.Actions() {
		if p, ok := a.(clienttesting.PatchAction); ok {
			out = append(out, p)
		}
	}
	return out
}

func deleteGracePeriods(dyn *dynamicfake.FakeDynamicClient) []*int64 {
	var out []*int64
	for _, a := range dyn.Actions() {
		if d, ok := a.(clienttesting.DeleteAction); ok {
			out = append(out, d.GetDeleteOptions().GracePeriodSeconds)
		}
	}
	return out
}

// Force on a resource that is not terminating yet must give the owning
// controller the normal delete first. Removing finalizers up front is what
// orphans external resources such as a Karpenter node's cloud instance.
func TestDeleteResource_ForceOnLiveResourceKeepsFinalizers(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, false))

	res, err := mgr.DeleteResourceWithResult(context.Background(), deleteOpts(true))
	if err != nil {
		t.Fatalf("force delete: %v", err)
	}
	if p := patchActions(dyn); len(p) != 0 {
		t.Fatalf("force delete patched a resource that was not terminating yet: %s", p[0].GetPatch())
	}
	if res.DeletionTimestamp == nil || !reflect.DeepEqual(res.PendingFinalizers, []string{"example.com/hold"}) {
		t.Fatalf("want terminating on [example.com/hold], got %+v", res)
	}
	if len(res.RemovedFinalizers) != 0 {
		t.Fatalf("removed finalizers = %v, want none", res.RemovedFinalizers)
	}
	graces := deleteGracePeriods(dyn)
	if len(graces) != 1 || graces[0] == nil || *graces[0] != 0 {
		t.Fatalf("force delete must send grace period 0, got %v", graces)
	}
}

func TestDeleteResource_ForceOnTerminatingResourceRemovesFinalizers(t *testing.T) {
	finalizers := []string{"example.com/hold", "example.com/other"}
	mgr, dyn := newDeleteFixture(t, mkConfigMap(finalizers, true))

	res, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/other", "example.com/hold"))
	if err != nil {
		t.Fatalf("force delete: %v", err)
	}
	if !gone(res) {
		t.Fatalf("want the resource reported gone, got %+v", res)
	}
	if !reflect.DeepEqual(res.RemovedFinalizers, finalizers) {
		t.Fatalf("removed finalizers = %v, want %v", res.RemovedFinalizers, finalizers)
	}
	patches := patchActions(dyn)
	if len(patches) != 1 {
		t.Fatalf("want exactly one patch, got %d", len(patches))
	}
	if patches[0].GetPatchType() != types.JSONPatchType {
		t.Fatalf("patch type = %s, want JSON patch with a test op", patches[0].GetPatchType())
	}
	body := string(patches[0].GetPatch())
	if !strings.Contains(body, `"op":"test"`) || !strings.Contains(body, `"example.com/hold"`) {
		t.Fatalf("patch does not assert the finalizers it removes: %s", body)
	}
	if _, err := dyn.Tracker().Get(configMapGVR, "ns", "held"); !apierrors.IsNotFound(err) {
		t.Fatalf("object still present after force delete removed its finalizers (err=%v)", err)
	}
}

// The API server rejects a JSON patch whose test op fails with 422. The
// response must say nothing was removed, never claim the stale list.
func TestDeleteResource_ForceFinalizersChangedRemovesNothing(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
	dyn.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		// A controller adds a finalizer between Radar's read and the patch.
		cur, err := dyn.Tracker().Get(configMapGVR, "ns", "held")
		if err != nil {
			return true, nil, err
		}
		u := cur.(*unstructured.Unstructured)
		u.SetFinalizers([]string{"example.com/hold", "example.com/new"})
		if err := dyn.Tracker().Update(configMapGVR, u, "ns"); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewGenericServerResponse(http.StatusUnprocessableEntity, "patch", schema.GroupResource{Resource: "configmaps"}, "held", "test operation failed", 0, false)
	})

	_, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
	if !errors.Is(err, ErrFinalizersChanged) {
		t.Fatalf("want ErrFinalizersChanged, got %v", err)
	}
	if graces := deleteGracePeriods(dyn); len(graces) != 0 {
		t.Fatal("delete was sent after the finalizer patch failed")
	}
}

func TestDeleteResource_ForcePatchForbidden(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
	dyn.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "held", nil)
	})

	_, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
	if !apierrors.IsForbidden(err) {
		t.Fatalf("want a Forbidden error the handler can map to 403, got %v", err)
	}
}

func TestDeleteResource_ForceAlreadyGoneIsDeleted(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap(nil, false))
	if err := dyn.Tracker().Delete(configMapGVR, "ns", "held"); err != nil {
		t.Fatal(err)
	}

	res, err := mgr.DeleteResourceWithResult(context.Background(), deleteOpts(true))
	if err != nil {
		t.Fatalf("force delete of a missing object: %v", err)
	}
	if !gone(res) {
		t.Fatalf("want the resource reported gone, got %+v", res)
	}
}

// Force delete must not newly require get. A caller who may delete but not
// read still gets the grace-0 delete, and finalizers Radar could not read are
// never removed.
func TestDeleteResource_ForceWithoutGetStillDeletes(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
	dyn.PrependReactor("get", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "held", nil)
	})

	res, err := mgr.DeleteResourceWithResult(context.Background(), deleteOpts(true))
	if err != nil {
		t.Fatalf("force delete without get permission: %v", err)
	}
	if p := patchActions(dyn); len(p) != 0 {
		t.Fatalf("removed finalizers Radar could not read: %s", p[0].GetPatch())
	}
	graces := deleteGracePeriods(dyn)
	if len(graces) != 1 || graces[0] == nil || *graces[0] != 0 {
		t.Fatalf("want one grace-0 delete, got %v", graces)
	}
	if res.ObservationError == "" || len(res.RemovedFinalizers) != 0 {
		t.Fatalf("want an unobserved delete with nothing removed, got %+v", res)
	}
}

// The dialog showed a live resource, so the user agreed to a grace-0 delete
// only. If the resource started terminating before they confirmed, a plain
// force request must still not remove its finalizers.
func TestDeleteResource_ForceWithoutConsentNeverRemovesFinalizers(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))

	res, err := mgr.DeleteResourceWithResult(context.Background(), deleteOpts(true))
	if err != nil {
		t.Fatalf("force delete: %v", err)
	}
	if p := patchActions(dyn); len(p) != 0 {
		t.Fatalf("removed finalizers without consent: %s", p[0].GetPatch())
	}
	if res.DeletionTimestamp == nil || !reflect.DeepEqual(res.PendingFinalizers, []string{"example.com/hold"}) {
		t.Fatalf("want terminating on [example.com/hold], got %+v", res)
	}
}

// Consent covers the finalizers the user was shown. A different list on the
// server fails closed before anything is patched or deleted.
func TestDeleteResource_ConsentForDifferentFinalizersFailsClosed(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold", "karpenter.sh/termination"}, true))

	_, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
	if !errors.Is(err, ErrFinalizersChanged) {
		t.Fatalf("want ErrFinalizersChanged, got %v", err)
	}
	if p := patchActions(dyn); len(p) != 0 {
		t.Fatalf("patched despite a changed finalizer list: %s", p[0].GetPatch())
	}
	if g := deleteGracePeriods(dyn); len(g) != 0 {
		t.Fatal("deleted despite a changed finalizer list")
	}
}

// Consent never overrides the rule that the controller gets the normal delete
// first: on a live resource it is a grace-0 delete only.
func TestDeleteResource_ConsentOnLiveResourceKeepsFinalizers(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, false))

	res, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
	if err != nil {
		t.Fatalf("force delete: %v", err)
	}
	if p := patchActions(dyn); len(p) != 0 {
		t.Fatalf("removed finalizers from a live resource: %s", p[0].GetPatch())
	}
	if res.DeletionTimestamp == nil || len(res.PendingFinalizers) == 0 {
		t.Fatalf("want terminating with its finalizers, got %+v", res)
	}
}

func deleteActions(dyn *dynamicfake.FakeDynamicClient) []clienttesting.DeleteAction {
	var out []clienttesting.DeleteAction
	for _, a := range dyn.Actions() {
		if d, ok := a.(clienttesting.DeleteAction); ok {
			out = append(out, d)
		}
	}
	return out
}

// Radar reads terminating object A, but by the time the finalizer patch runs
// the name belongs to live object B with the same finalizer. The patch must
// not touch B, and nothing may be deleted.
func TestDeleteResource_ReplacedBeforePatchIsNotTouched(t *testing.T) {
	b := mkConfigMap([]string{"example.com/hold"}, false)
	b.SetUID("uid-b")
	mgr, dyn := newDeleteFixture(t, b)
	a := mkConfigMap([]string{"example.com/hold"}, true)
	served := false
	dyn.PrependReactor("get", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		if served {
			return false, nil, nil
		}
		served = true
		return true, a.DeepCopy(), nil
	})

	// The patch's UID test fails with 422 and a new UID is there now. That is
	// also what a retried patch sees after its first attempt removed A's
	// finalizers and A was replaced, so Radar can't say nothing was removed.
	_, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
	if !errors.Is(err, ErrFinalizerRemovalUnknown) {
		t.Fatalf("want ErrFinalizerRemovalUnknown, got %v", err)
	}
	cur, gerr := dyn.Tracker().Get(configMapGVR, "ns", "held")
	if gerr != nil {
		t.Fatalf("object B is gone: %v", gerr)
	}
	if f := cur.(*unstructured.Unstructured).GetFinalizers(); !reflect.DeepEqual(f, []string{"example.com/hold"}) {
		t.Fatalf("object B lost its finalizers: %v", f)
	}
	if d := deleteActions(dyn); len(d) != 0 {
		t.Fatal("deleted after the object was replaced")
	}
}

// Every delete is pinned to the object Radar read, so a replacement created
// between the read (or finalizer patch) and the delete is refused by the API
// server instead of deleted.
func TestDeleteResource_DeleteIsPinnedToTheUIDRead(t *testing.T) {
	for name, opts := range map[string]DeleteResourceOptions{
		"plain":            deleteOpts(false),
		"force":            deleteOpts(true),
		"force-with-strip": stripOpts("example.com/hold"),
	} {
		t.Run(name, func(t *testing.T) {
			mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
			if _, err := mgr.DeleteResourceWithResult(context.Background(), opts); err != nil {
				t.Fatalf("delete: %v", err)
			}
			d := deleteActions(dyn)
			if len(d) != 1 {
				t.Fatalf("want one delete, got %d", len(d))
			}
			pre := d[0].GetDeleteOptions().Preconditions
			if pre == nil || pre.UID == nil || *pre.UID != "uid-a" {
				t.Fatalf("delete not pinned to uid-a: %+v", pre)
			}
		})
	}
}

// The UI sends the UID it showed. A different object under that name is not
// the one the user agreed to delete.
func TestDeleteResource_ExpectedUIDMismatchDeletesNothing(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
	opts := stripOpts("example.com/hold")
	opts.ExpectedUID = "uid-shown-in-ui"

	_, err := mgr.DeleteResourceWithResult(context.Background(), opts)
	if !errors.Is(err, ErrResourceChanged) {
		t.Fatalf("want ErrResourceChanged, got %v", err)
	}
	if len(patchActions(dyn)) != 0 || len(deleteActions(dyn)) != 0 {
		t.Fatal("patched or deleted an object the user was not shown")
	}
}

// Without get permission there is nothing read to pin to, so the UID the UI
// showed is the precondition.
func TestDeleteResource_ExpectedUIDPinsDeleteWithoutGet(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap(nil, false))
	dyn.PrependReactor("get", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "held", nil)
	})
	opts := deleteOpts(true)
	opts.ExpectedUID = "uid-a"
	if _, err := mgr.DeleteResourceWithResult(context.Background(), opts); err != nil {
		t.Fatalf("delete: %v", err)
	}
	pre := deleteActions(dyn)[0].GetDeleteOptions().Preconditions
	if pre == nil || pre.UID == nil || *pre.UID != "uid-a" {
		t.Fatalf("delete not pinned to the UID the UI showed: %+v", pre)
	}
}

// The finalizer patch succeeded, so something was written. If the pinned
// delete then finds a different object under the name, the original is gone
// (it lost its last finalizer while terminating), and the result must say
// what was removed rather than "nothing was deleted".
func TestDeleteResource_ReplacedAfterFinalizerPatchReportsTheWrite(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
	dyn.PrependReactor("delete", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "held", errors.New("the UID in the precondition does not match"))
	})

	res, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
	if err != nil {
		t.Fatalf("want a result after a successful finalizer patch, got error %v", err)
	}
	if !reflect.DeepEqual(res.RemovedFinalizers, []string{"example.com/hold"}) {
		t.Fatalf("removed finalizers lost: %v", res.RemovedFinalizers)
	}
}

// Any failure of the delete call after the finalizer patch landed must still
// report the removal: the write is done and cannot be taken back.
func TestDeleteResource_DeleteFailingAfterFinalizerPatchReportsTheWrite(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
	dyn.PrependReactor("delete", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "held", errors.New("admission denied"))
	})

	res, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
	if err != nil {
		t.Fatalf("want a result after a successful finalizer patch, got error %v", err)
	}
	if !reflect.DeepEqual(res.RemovedFinalizers, []string{"example.com/hold"}) {
		t.Fatalf("removed finalizers lost: %v", res.RemovedFinalizers)
	}
}

// Consent is to remove finalizers from the object the user was shown. Without
// its UID, a same-name replacement with the same finalizers would qualify.
func TestDeleteResource_ConsentWithoutUIDIsRejected(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
	opts := stripOpts("example.com/hold")
	opts.ExpectedUID = ""

	_, err := mgr.DeleteResourceWithResult(context.Background(), opts)
	if !errors.Is(err, ErrConsentWithoutUID) {
		t.Fatalf("want ErrConsentWithoutUID, got %v", err)
	}
	if len(patchActions(dyn)) != 0 || len(deleteActions(dyn)) != 0 {
		t.Fatal("patched or deleted without the UID the consent was given for")
	}
}

// Consent to remove finalizers can't be honored without reading them, and a
// grace-0 delete alone does nothing to a terminating resource, so reporting
// success would be untrue.
func TestDeleteResource_ConsentWithUnreadableResourceFailsWithoutWriting(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
	dyn.PrependReactor("get", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewTimeoutError("read timed out", 1)
	})

	_, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
	if err == nil || !apierrors.IsTimeout(err) {
		t.Fatalf("want the read error, got %v", err)
	}
	if len(patchActions(dyn)) != 0 || len(deleteActions(dyn)) != 0 {
		t.Fatal("wrote after failing to read the finalizers the user agreed to remove")
	}
}

// Only a failed UID precondition means the name now belongs to another
// object. Any other conflict must not be reported as a replacement.
func TestDeleteResource_OtherConflictIsNotReportedAsReplaced(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, false))
	dyn.PrependReactor("delete", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "held", errors.New("the object has been modified"))
	})

	_, err := mgr.DeleteResourceWithResult(context.Background(), deleteOpts(true))
	if err == nil || errors.Is(err, ErrResourceChanged) {
		t.Fatalf("want a plain delete failure, got %v", err)
	}
}

// A patch can be applied and its response lost, or still be in flight when
// the error arrives. The result must not then say that nothing was removed.
func TestDeleteResource_PatchOutcomeUnknownIsNotReportedAsNothingRemoved(t *testing.T) {
	for name, applied := range map[string]bool{"applied, response lost": true, "not applied": false} {
		t.Run(name, func(t *testing.T) {
			mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
			dyn.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
				if applied {
					cur, err := dyn.Tracker().Get(configMapGVR, "ns", "held")
					if err != nil {
						return true, nil, err
					}
					u := cur.(*unstructured.Unstructured)
					u.SetFinalizers(nil)
					if err := dyn.Tracker().Update(configMapGVR, u, "ns"); err != nil {
						return true, nil, err
					}
				}
				return true, nil, apierrors.NewTimeoutError("patch response lost", 1)
			})

			_, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
			if errors.Is(err, ErrFinalizersChanged) {
				t.Fatalf("reported that nothing was removed after an ambiguous failure: %v", err)
			}
			// Even when the object still looks untouched, the patch may land
			// after the read, so the outcome stays unknown.
			if !errors.Is(err, ErrFinalizerRemovalUnknown) {
				t.Fatalf("applied=%v, want ErrFinalizerRemovalUnknown, got %v", applied, err)
			}
			if len(deleteActions(dyn)) != 0 {
				t.Fatal("deleted after the finalizer patch failed")
			}
		})
	}
}

// Both ways the API server reports a failed UID precondition mean the name
// now belongs to another object.
func TestDeleteResource_UIDPreconditionConflictIsReplaced(t *testing.T) {
	for _, msg := range []string{
		"the UID in the precondition (uid-a) does not match the UID in record (uid-b). The object might have been deleted and then recreated",
		"Precondition failed: UID in precondition: uid-a, UID in object meta: uid-b",
	} {
		mgr, dyn := newDeleteFixture(t, mkConfigMap(nil, false))
		dyn.PrependReactor("delete", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "held", errors.New(msg))
		})
		if _, err := mgr.DeleteResourceWithResult(context.Background(), deleteOpts(false)); !errors.Is(err, ErrResourceChanged) {
			t.Fatalf("%q: want ErrResourceChanged, got %v", msg, err)
		}
	}
}

// A mutating webhook can keep a finalizer through the patch. Only the
// finalizers actually gone from the stored object are reported as removed.
func TestDeleteResource_ReportsOnlyFinalizersThePatchRemoved(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold", "example.com/kept"}, true))
	dyn.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		cur, err := dyn.Tracker().Get(configMapGVR, "ns", "held")
		if err != nil {
			return true, nil, err
		}
		u := cur.(*unstructured.Unstructured)
		u.SetFinalizers([]string{"example.com/kept"})
		return true, u, dyn.Tracker().Update(configMapGVR, u, "ns")
	})

	res, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold", "example.com/kept"))
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !reflect.DeepEqual(res.RemovedFinalizers, []string{"example.com/hold"}) {
		t.Fatalf("removed finalizers = %v, want only example.com/hold", res.RemovedFinalizers)
	}
}

// A definite rejection, such as an admission webhook denying the patch, wrote
// nothing, so it is reported as such rather than as an unknown outcome.
func TestDeleteResource_RejectedPatchSaysNothingWasRemoved(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
	dyn.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewBadRequest("admission webhook denied the request")
	})

	_, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
	if err == nil || errors.Is(err, ErrFinalizerRemovalUnknown) || !strings.Contains(err.Error(), "none were removed") {
		t.Fatalf("want a rejection that says none were removed, got %v", err)
	}
	if len(deleteActions(dyn)) != 0 {
		t.Fatal("deleted after the finalizer patch was rejected")
	}
}

// The test op can fail because the object is gone: its controller finished,
// or an earlier attempt of a retried patch removed the finalizers. Neither is
// a changed list, and only the first means nothing was removed, so the
// outcome is unknown.
func TestDeleteResource_GoneDuringPatchIsNotReportedAsChanged(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
	dyn.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		if err := dyn.Tracker().Delete(configMapGVR, "ns", "held"); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewInvalid(schema.GroupKind{Kind: "ConfigMap"}, "held", nil)
	})

	_, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
	if !errors.Is(err, ErrFinalizerRemovalUnknown) {
		t.Fatalf("want ErrFinalizerRemovalUnknown, got %v", err)
	}
}

// client-go retries a patch that got a 5xx with Retry-After. If the first
// attempt removed the finalizers, the retry's test op fails (422) or finds the
// object gone (404). Neither may be reported as "nothing was removed".
func TestDeleteResource_RetriedPatchAfterRemovalIsUnknown(t *testing.T) {
	for name, lastErr := range map[string]error{
		"retry finds the list emptied":      apierrors.NewInvalid(schema.GroupKind{Kind: "ConfigMap"}, "held", nil),
		"retry finds the object gone":       apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "held"),
		"retry finds one kept by a webhook": apierrors.NewInvalid(schema.GroupKind{Kind: "ConfigMap"}, "held", nil),
	} {
		t.Run(name, func(t *testing.T) {
			mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold", "example.com/other"}, true))
			dyn.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
				cur, err := dyn.Tracker().Get(configMapGVR, "ns", "held")
				if err != nil {
					return true, nil, err
				}
				u := cur.(*unstructured.Unstructured)
				var kept []string
				if strings.Contains(name, "webhook") {
					kept = []string{"example.com/other"}
				}
				u.SetFinalizers(kept)
				if err := dyn.Tracker().Update(configMapGVR, u, "ns"); err != nil {
					return true, nil, err
				}
				return true, nil, lastErr
			})

			_, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold", "example.com/other"))
			if !errors.Is(err, ErrFinalizerRemovalUnknown) {
				t.Fatalf("want ErrFinalizerRemovalUnknown, got %v", err)
			}
		})
	}
}

// A 422 can also come from validating the update itself (a CRD schema or CEL
// rule). With the object unchanged that is not a changed finalizer list.
func TestDeleteResource_ValidationRejectionIsNotReportedAsChanged(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
	dyn.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInvalid(schema.GroupKind{Kind: "ConfigMap"}, "held", nil)
	})

	_, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
	if err == nil || errors.Is(err, ErrFinalizersChanged) || !strings.Contains(err.Error(), "none were removed") {
		t.Fatalf("want a rejection that says none were removed, got %v", err)
	}
}

// client-go retries a patch that got a 5xx with Retry-After. If the first
// attempt removed the finalizers, the original is gone, and a controller
// recreated the name before the retry, the retry fails its UID test with 422
// and a new UID is there. Whether the original's finalizers were removed is
// unknown, so Radar must not say nothing was removed.
func TestDeleteResource_RetriedPatchAfterReplacementIsUnknown(t *testing.T) {
	mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
	dyn.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		// The first attempt landed: A lost its last finalizer and is gone.
		if err := dyn.Tracker().Delete(configMapGVR, "ns", "held"); err != nil {
			return true, nil, err
		}
		replacement := mkConfigMap([]string{"example.com/hold"}, false)
		replacement.SetUID("uid-b")
		if err := dyn.Tracker().Create(configMapGVR, replacement, "ns"); err != nil {
			return true, nil, err
		}
		// The retry's answer is what the caller gets.
		return true, nil, apierrors.NewInvalid(schema.GroupKind{Kind: "ConfigMap"}, "held", nil)
	})

	_, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
	if !errors.Is(err, ErrFinalizerRemovalUnknown) {
		t.Fatalf("want ErrFinalizerRemovalUnknown, got %v", err)
	}
	if strings.Contains(err.Error(), "nothing was deleted") || strings.Contains(err.Error(), "none were removed") {
		t.Fatalf("claims nothing happened: %v", err)
	}
}

// A rejection is the last attempt's answer. If an earlier, retried attempt
// removed the finalizers (and the object went away), "none were removed"
// would be false, so a rejection is checked against what is there now.
func TestDeleteResource_RetriedPatchEndingInRejectionIsUnknown(t *testing.T) {
	for name, rejection := range map[string]error{
		"throttled": apierrors.NewTooManyRequests("slow down", 1),
		"forbidden": apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "held", errors.New("denied")),
	} {
		t.Run(name, func(t *testing.T) {
			mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold"}, true))
			dyn.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
				// The first attempt landed: A lost its last finalizer and is gone.
				if err := dyn.Tracker().Delete(configMapGVR, "ns", "held"); err != nil {
					return true, nil, err
				}
				return true, nil, rejection
			})
			_, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold"))
			if !errors.Is(err, ErrFinalizerRemovalUnknown) {
				t.Fatalf("want ErrFinalizerRemovalUnknown, got %v", err)
			}
		})
	}
}

// A rejection says none were removed only when the object Radar read still
// holds every finalizer the user saw: not a same-name replacement holding the
// same list, and not the original missing some of them.
func TestDeleteResource_RejectionCheckedAgainstTheObjectRead(t *testing.T) {
	for name, after := range map[string]func(*dynamicfake.FakeDynamicClient) error{
		"replaced, same finalizers": func(dyn *dynamicfake.FakeDynamicClient) error {
			replacement := mkConfigMap([]string{"example.com/hold", "example.com/other"}, true)
			replacement.SetUID("uid-b")
			return dyn.Tracker().Update(configMapGVR, replacement, "ns")
		},
		"some finalizers gone": func(dyn *dynamicfake.FakeDynamicClient) error {
			kept := mkConfigMap([]string{"example.com/other"}, true)
			return dyn.Tracker().Update(configMapGVR, kept, "ns")
		},
	} {
		t.Run(name, func(t *testing.T) {
			mgr, dyn := newDeleteFixture(t, mkConfigMap([]string{"example.com/hold", "example.com/other"}, true))
			dyn.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
				if err := after(dyn); err != nil {
					t.Errorf("setting up the object after the patch: %v", err)
				}
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "held", errors.New("denied"))
			})
			_, err := mgr.DeleteResourceWithResult(context.Background(), stripOpts("example.com/hold", "example.com/other"))
			if !errors.Is(err, ErrFinalizerRemovalUnknown) {
				t.Fatalf("want ErrFinalizerRemovalUnknown, got %v", err)
			}
		})
	}
}
