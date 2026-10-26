package server

import (
	"net/url"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSearchNamespaceParametersMatchNamedTerms(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "hidden"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "present", Namespace: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "neighbor", Namespace: "hidden"}},
	))
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), []k8s.APIResource{
		{Name: "pods", Kind: "Pod", Version: "v1", Namespaced: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dc := k8s.GetDynamicResourceCache()
	discovered := make(chan struct{})
	dc.OnCRDDiscoveryComplete(func() { close(discovered) })
	dc.DiscoverAllCRDs()
	select {
	case <-discovered:
	case <-time.After(5 * time.Second):
		t.Fatal("CRD discovery did not complete")
	}
	env := newAuthTestServer(t)
	env.srv.permCache.Set("restricted", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})
	env.srv.permCache.Set("unrestricted", nil, &auth.UserPermissions{})
	env.srv.permCache.Set("no-access", nil, &auth.UserPermissions{AllowedNamespaces: []string{}})

	for _, tc := range []struct {
		name, user, pin, terms, value, control string
		wantHits                               int
		wantCode                               readreason.Code
	}{
		{"hidden", "restricted", "", "", "hidden", "ns:hidden", 0, readreason.UserNamespaceNotVisible},
		{"missing", "unrestricted", "", "", "missing", "ns:missing", 0, readreason.NamespaceNotFound},
		{"outside pin", "unrestricted", "default", "", "hidden", "ns:hidden", 0, readreason.OutsideNamespacePin},
		{"readable and hidden", "restricted", "", "ns:default", "hidden", "ns:default ns:hidden", 1, readreason.UserNamespaceNotVisible},
		{"readable and missing", "unrestricted", "", "ns:default", "missing", "ns:default ns:missing", 1, readreason.NamespaceNotFound},
		{"readable and outside pin", "unrestricted", "default", "ns:default", "hidden", "ns:default ns:hidden", 1, readreason.OutsideNamespacePin},
		{"unrestricted named", "unrestricted", "", "", "default", "ns:default", 1, ""},
		{"readable union", "unrestricted", "", "ns:default", "hidden", "ns:default ns:hidden", 2, ""},
		{"duplicate names", "unrestricted", "", "ns:default", "default", "ns:default", 1, ""},
		{"no namespace access", "no-access", "", "", "hidden", "ns:hidden", 0, readreason.UserNamespaceNotVisible},
		{"hidden pin", "restricted", "hidden", "", "hidden", "ns:hidden", 0, readreason.UserNamespaceNotVisible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k8s.ForceNamespaceScope = tc.pin != ""
			k8s.SetFallbackNamespace(tc.pin)
			t.Cleanup(func() { k8s.ForceNamespaceScope = false; k8s.SetFallbackNamespace("") })
			control := searchAs(t, env, tc.user, "q="+url.QueryEscape("kind:Pod "+tc.control))
			if control.Total != tc.wantHits || len(control.Hits) != tc.wantHits {
				t.Fatalf("named-term hits = %+v, want %d", control, tc.wantHits)
			}
			if tc.wantCode == "" {
				if control.Coverage.Status != resourceapi.CoverageComplete {
					t.Fatalf("named-term coverage = %+v", control.Coverage)
				}
			} else if control.Coverage.Status != resourceapi.CoveragePartial || len(control.Coverage.Gaps) != 1 || control.Coverage.Gaps[0].Code != tc.wantCode {
				t.Fatalf("named-term gaps = %+v, want %s", control.Coverage, tc.wantCode)
			}
			for _, parameter := range []string{"namespace", "namespaces"} {
				for _, global := range []string{"", "&globalNs=1"} {
					t.Run(parameter+global, func(t *testing.T) {
						query := "q=" + url.QueryEscape("kind:Pod "+tc.terms) + "&" + parameter + "=" + url.QueryEscape(tc.value) + global
						result := searchAs(t, env, tc.user, query)
						if !reflect.DeepEqual(result.Coverage, control.Coverage) || !slices.Equal(hitKeys(result), hitKeys(control)) || result.Searched != control.Searched || result.TotalMatched != control.TotalMatched {
							t.Fatalf("parameter result = %+v, named-term result = %+v", result, control)
						}
						if slices.Contains(result.Coverage.NarrowedBy, resourceapi.NarrowedByUserAccess) {
							t.Fatalf("named namespaces narrowed by user_access: %+v", result.Coverage)
						}
					})
				}
			}
			if tc.terms == "ns:default" && tc.value != "default" {
				result := searchAs(t, env, tc.user, "q=kind:Pod&namespaces=default,"+tc.value)
				if !reflect.DeepEqual(result.Coverage, control.Coverage) || !slices.Equal(hitKeys(result), hitKeys(control)) || result.Searched != control.Searched {
					t.Fatalf("plural list = %+v, named-term result = %+v", result, control)
				}
			}
		})
	}
}
