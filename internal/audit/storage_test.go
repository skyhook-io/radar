package audit

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestStorageScopeResolvesTypedClusterGrants(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		var seen sync.Map
		scope := resolveReadScope([]string{"app"}, nil, nil, func(group, resource, ns string) bool {
			if ns != "" {
				t.Fatalf("cluster grant checked in namespace %q", ns)
			}
			seen.Store(schema.GroupResource{Group: group, Resource: resource}.String(), true)
			return allowed
		})
		for _, gvr := range storageClusterResources {
			_, checked := seen.Load(gvr.GroupResource().String())
			if !checked || scope.allows(gvr, "") != allowed {
				t.Fatalf("missing exact grant for %s: %+v", gvr, scope)
			}
		}
	}
}

func TestStorageCacheScopeAndCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, podNS            string
		pods, pvGrant, scGrant bool
		consumerFinding        bool
	}{
		{"complete", "", true, true, true, true},
		{"denied Pods", "", false, true, true, false},
		{"Pods in different namespace", "other", true, true, true, false},
		{"Pods in subject namespace", "app", true, true, true, true},
		{"denied PV", "", true, false, true, false},
		{"denied StorageClass", "", true, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "app"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "bound"}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
			pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "released", UID: "released-uid"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}}
			hidden := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "hidden", Namespace: "other"}, InvolvedObject: corev1.ObjectReference{Kind: "PersistentVolume", APIVersion: "v1", Name: "released", UID: "released-uid"}, Type: corev1.EventTypeWarning, Reason: "VolumeFailedDelete", Message: "private failure", LastTimestamp: metav1.NewTime(time.Now())}
			scopes := map[string]k8score.ResourceScope{}
			for _, resource := range append(slices.Clone(typedPVCConsumers), k8score.PersistentVolumeClaims, k8score.PersistentVolumes, k8score.StorageClasses, k8score.Events) {
				scopes[resource] = k8score.ResourceScope{Enabled: true}
			}
			scopes[k8score.Pods] = k8score.ResourceScope{Enabled: tc.pods, Namespace: tc.podNS}
			if err := k8s.InitScopedTestResourceCache(fake.NewClientset(pvc, pv, hidden), scopes); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			scope := &ReadScope{Namespaces: []string{"app"}, ClusterResources: map[string]bool{"persistentvolumes": tc.pvGrant, "storageclasses.storage.k8s.io": tc.scGrant}}
			r := RunFromCache(k8s.GetResourceCache(), []string{"app"}, &RunOptions{Scope: scope})
			consumerFinding := false
			for _, f := range r.Findings {
				if strings.Contains(f.Message, "private failure") || f.CheckID == "releasedPV" {
					t.Fatalf("hidden event leaked: %+v", f)
				}
				consumerFinding = consumerFinding || f.CheckID == "pvcNoConsumer"
			}
			if consumerFinding != tc.consumerFinding {
				t.Fatalf("consumer finding=%v want=%v: %+v", consumerFinding, tc.consumerFinding, r)
			}
			if !tc.pvGrant && !slices.Contains(r.MissingInputs, "persistentvolumes") {
				t.Fatal("PV denial lost")
			}
			if (!tc.pods || tc.podNS == "other") && (!slices.Contains(r.MissingInputs, "pvc-consumers") || r.CheckCounts["pvcNoConsumer"].Evaluated != 0) {
				t.Fatal("unknown consumer coverage became passing")
			}
			if tc.pvGrant && !slices.Contains(r.MissingInputs, "pv-deletion-events") {
				t.Fatal("partial event coverage lost")
			}
		})
	}
}

func TestStorageVisiblePVWarningWithPartialEvents(t *testing.T) {
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "disk", UID: "disk-uid"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased}}
	event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "failure", Namespace: "app"}, InvolvedObject: corev1.ObjectReference{Kind: "PersistentVolume", APIVersion: "v1", Name: "disk", UID: "disk-uid"}, Type: corev1.EventTypeWarning, Reason: "VolumeFailedDelete", Message: "visible failure", LastTimestamp: metav1.NewTime(time.Now())}
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(pv, event), map[string]k8score.ResourceScope{k8score.PersistentVolumes: {Enabled: true}, k8score.Events: {Enabled: true, Namespace: "app"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	input := collectStorageInput(k8s.GetResourceCache(), []string{"app"}, &ReadScope{Namespaces: []string{"app"}, ClusterResources: map[string]bool{"persistentvolumes": true}})
	r := bp.RunChecks(input)
	if len(r.Findings) != 1 || r.Findings[0].CheckID != "releasedPV" || !strings.Contains(r.Findings[0].Message, "visible failure") {
		t.Fatalf("visible warning lost: %+v", r)
	}
}

func TestStorageUnsyncedCacheDoesNotAssertAbsence(t *testing.T) {
	resources := map[string]bool{k8score.PersistentVolumeClaims: true, k8score.PersistentVolumes: true, k8score.StorageClasses: true, k8score.Events: true}
	for _, resource := range typedPVCConsumers {
		resources[resource] = true
	}
	client := fake.NewClientset()
	client.PrependReactor("list", "*", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("initial list unavailable")
	})
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: client, ResourceTypes: resources, SyncTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}
	input := collectStorageInput(cache, nil, nil)
	if input.PersistentVolumeClaims != nil || input.PersistentVolumes != nil || input.StorageClasses != nil || input.Events != nil || input.PVDeletionEventsComplete || len(input.PVCConsumerNamespaces) != 0 {
		t.Fatalf("unsynced cache claims coverage: %+v", input)
	}
}
