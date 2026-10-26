package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestDynamicListKeepsWholeScopeCredentialRejection(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(testNamespaces("read-a", "read-b")...))
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"},
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"name": "present", "namespace": "read-a"}}})
	var rejected atomic.Bool
	dyn.PrependReactor("list", "widgets", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() == "" {
			if rejected.Load() {
				return true, nil, apierrors.NewUnauthorized("expired collector credentials")
			}
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", fmt.Errorf("denied"))
		}
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	d := k8s.GetDynamicResourceCache()
	d.Stop()
	core, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: k8s.GetResourceDiscovery().ResourceDiscovery, NamespaceFallbacks: []string{"read-a", "read-b"}, ProbeRetryBackoff: []time.Duration{time.Hour}, ProbeRecoveryIntervals: []time.Duration{time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	d.DynamicResourceCache = core
	if _, err := d.List(gvr, "read-a"); err != nil {
		t.Fatal(err)
	}
	if !d.WaitForSync(gvr, 3*time.Second) {
		t.Fatal("read-a did not sync")
	}
	rejected.Store(true)
	resp := get(t, "/api/resources/widgets?group=example.test")
	defer resp.Body.Close()
	var body struct {
		Items    []map[string]any      `json:"items"`
		Coverage *resourceapi.Coverage `json:"coverage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(body)
	// read-a's rows stand; the rest of the scope is unread because Radar's
	// credentials were rejected, which is not collection narrowing.
	if resp.StatusCode != http.StatusOK || len(body.Items) != 1 || body.Coverage == nil || body.Coverage.Status != resourceapi.CoveragePartial {
		t.Fatalf("whole-scope credential rejection was lost: %s", encoded)
	}
	if !slices.ContainsFunc(body.Coverage.Gaps, func(g resourceapi.Gap) bool {
		return g.Code == readreason.RadarCredentialsRejected && g.Namespaces != nil && g.Namespaces.Extent == resourceapi.ExtentUnknown
	}) {
		t.Fatalf("want a radar_credentials_rejected gap over the unknown remainder: %s", encoded)
	}
}
