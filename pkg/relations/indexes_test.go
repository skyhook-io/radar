package relations

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestWideReverseFamiliesDeclareIndexOrScanReason(t *testing.T) {
	cluster := map[resourceid.GroupKind]bool{
		{Group: "gateway.networking.k8s.io", Kind: "GatewayClass"}: true,
		{Group: "cert-manager.io", Kind: "ClusterIssuer"}:          true,
		{Group: "keda.sh", Kind: "ClusterTriggerAuthentication"}:   true,
		{Group: "argoproj.io", Kind: "ClusterWorkflowTemplate"}:    true,
	}
	namespaced := map[resourceid.GroupKind]bool{
		{Group: "argoproj.io", Kind: "Rollout"}:           true,
		{Group: "argoproj.io", Kind: "WorkflowTemplate"}:  true,
		{Group: "argoproj.io", Kind: "Workflow"}:          true,
		{Group: "argoproj.io", Kind: "CronWorkflow"}:      true,
		{Group: "argoproj.io", Kind: "AnalysisRun"}:       true,
		{Group: "keda.sh", Kind: "TriggerAuthentication"}: true,
		{Group: "cert-manager.io", Kind: "Issuer"}:        true,
		{Group: "serving.knative.dev", Kind: "Revision"}:  true,
	}
	for _, b := range resourceid.Builtins {
		if b.Namespaced {
			namespaced[b.GroupKind()] = true
		}
		if !b.Namespaced {
			cluster[b.GroupKind()] = true
		}
	}
	for _, f := range Families("") {
		wide := f.CrossNamespace || f.IsTarget != nil
		for _, target := range f.Targets {
			if !wide && !cluster[target] && !namespaced[target] {
				t.Errorf("%s target %v has unclassified scope", f.Name, target)
			}
			wide = wide || cluster[target]
		}
		if wide && f.ReferrersIndex == nil && strings.TrimSpace(f.ScanAccepted) == "" {
			t.Errorf("%s scans widely without an index or accepted reason", f.Name)
		}
		if f.ReferrersIndex != nil && f.ScanAccepted != "" {
			t.Errorf("%s declares both index and accepted scan", f.Name)
		}
	}
}

func TestFamilyRegistryIncludesEveryReferenceFamily(t *testing.T) {
	names := map[string]bool{}
	for _, f := range Families("") {
		if names[f.Name] {
			t.Errorf("duplicate family %s", f.Name)
		}
		names[f.Name] = true
	}
	files, err := parser.ParseDir(token.NewFileSet(), ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for filename, file := range files["relations"].Files {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			typ, ok := lit.Type.(*ast.Ident)
			if !ok || typ.Name != "Family" {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				value, literal := kv.Value.(*ast.BasicLit)
				if ok && key.Name == "Name" && literal && !names[strings.Trim(value.Value, "\"")] {
					t.Errorf("%s has an unregistered family %s", filename, value.Value)
				}
			}
			return true
		})
	}
}

func TestReferrerIndexIncludesAlternativesAndDeduplicatesKeys(t *testing.T) {
	u := object("traefik.io/v1alpha1", "IngressRoute", "web", map[string]any{
		"routes": []any{map[string]any{"services": []any{map[string]any{"kind": "TraefikService", "name": "app", "namespace": "shared"}, map[string]any{"kind": "TraefikService", "name": "app", "namespace": "shared"}}}},
	})
	keys, err := candidateFamily(TraefikBackends).IndexFunc()(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %q; want one per API group", keys)
	}
	for _, group := range []string{"traefik.io", "traefik.containo.us"} {
		target := resourceid.NewRef(group, "TraefikService", "shared", "app")
		if !slices.Contains(keys, ReferrerKey(target)) {
			t.Errorf("missing %s", group)
		}
	}
}

func TestIstioIndexIsIndependentOfMeshDomain(t *testing.T) {
	for _, host := range []string{"db", "db.data", "db.data.svc.cluster.local", "db.data.svc.custom.internal"} {
		dr := object("networking.istio.io/v1", "DestinationRule", "db", map[string]any{"host": host})
		ns := "data"
		if host == "db" {
			ns = "shop"
		}
		target := resourceid.NewRef("", "Service", ns, "db")
		for _, domain := range []string{"", "cluster.local", "custom.internal"} {
			f := IstioServiceHosts(domain)
			keys, err := f.IndexFunc()(dr)
			if err != nil || !slices.Contains(keys, ReferrerKey(target)) {
				t.Errorf("host %s domain %s: %q, %v", host, domain, keys, err)
			}
		}
	}
}

type referrerReads struct {
	allNamespaces
	t      *testing.T
	target resourceid.Ref
}

func (s *referrerReads) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) IndexedRead {
	if index == GatewayParents.ReferrersIndex.Name {
		if want := []string{ReferrerKey(s.target)}; !slices.Equal(keys, want) {
			s.t.Errorf("referrers read %q; want only the subject's key, %q", keys, want)
		}
	}
	return s.allNamespaces.Indexed(gk, index, keys, scope)
}

// A restricted caller's query reads the subject's key in every namespace and
// lists the referrers it may read; one it may not read is withheld and said
// to be.
func TestRestrictedReferrersReadTheSubjectsKeyAndWithholdTheRest(t *testing.T) {
	route := object("gateway.networking.k8s.io/v1", "HTTPRoute", "route", map[string]any{"parentRefs": []any{map[string]any{"name": "gw"}}})
	elsewhere := inNamespace(object("gateway.networking.k8s.io/v1", "HTTPRoute", "elsewhere", map[string]any{"parentRefs": []any{map[string]any{"name": "gw", "namespace": "shop"}}}), "team")
	gw := object("gateway.networking.k8s.io/v1", "Gateway", "gw", nil)
	s := &referrerReads{allNamespaces: allNamespaces{cluster: cluster{objects: []runtime.Object{route, elsewhere, gw}}}, t: t, target: refOf(gw)}
	got := NewView(s, namespaceAccess{"shop", nil}).Referrers(GatewayParents, refOf(gw), nil)
	if len(got.Referrers) != 1 || got.Referrers[0].Ref != refOf(route) || !got.Coverage.Narrowed {
		t.Fatalf("got %+v", got)
	}
}

func TestIndexExtractionDoesNotMutateSources(t *testing.T) {
	for _, ex := range indexExamples() {
		before := ex.obj.DeepCopyObject()
		_, err := ex.family.IndexFunc()(ex.obj)
		if err != nil || !reflect.DeepEqual(before, ex.obj) {
			t.Errorf("%s changed object or failed: %v", ex.family.Name, err)
		}
	}
}

func candidateFamily(f *Family) *Family {
	if f.ReferrersIndex != nil {
		return f
	}
	candidate := *f
	candidate.ReferrersIndex = referrersIndex(f.Name)
	candidate.ScanAccepted = ""
	return &candidate
}

func TestIstioReferrersResolveIndexCandidatesWithCurrentDomain(t *testing.T) {
	dr := object("networking.istio.io/v1", "DestinationRule", "db", map[string]any{"host": "db.data.svc.custom.internal"})
	f := IstioServiceHosts("custom.internal")
	st := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc, f.ReferrersIndex.Name: f.IndexFunc()})
	if err := st.Add(dr); err != nil {
		t.Fatal(err)
	}
	s := stores{byKind: map[resourceid.GroupKind]cache.Indexer{kindDestinationRule: st}}
	target := resourceid.NewRef("", "Service", "data", "db")
	for _, domain := range []string{"custom.internal", "cluster.local", ""} {
		got := NewView(s, ownership.Unrestricted()).Referrers(IstioServiceHosts(domain), target, nil)
		want := 0
		if domain == "custom.internal" {
			want = 1
		}
		if len(got.Referrers) != want {
			t.Errorf("domain %s: %+v; want %d referrers", domain, got, want)
		}
	}
}

func TestIndexDoesNotWidenLocalTargetSearch(t *testing.T) {
	source := object("keda.sh/v1alpha1", "ScaledObject", "web", map[string]any{"triggers": []any{map[string]any{"authenticationRef": map[string]any{"name": "auth"}}}})
	target := object("keda.sh/v1alpha1", "TriggerAuthentication", "auth", nil)
	s := &localIndexReads{allNamespaces: allNamespaces{cluster: cluster{objects: []runtime.Object{source, target}}}, t: t}
	got := NewView(s, ownership.Unrestricted()).Referrers(KEDAAuthentication, refOf(target), nil)
	if len(got.Referrers) != 1 {
		t.Fatalf("got %+v", got)
	}
}

type localIndexReads struct {
	allNamespaces
	t *testing.T
}

func (s *localIndexReads) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) IndexedRead {
	if scope.AllNamespaces || scope.IncludeClusterScoped || !slices.Equal(scope.Namespaces, []string{"shop"}) {
		s.t.Errorf("local auth search widened: %+v", scope)
	}
	return s.allNamespaces.Indexed(gk, index, keys, scope)
}
