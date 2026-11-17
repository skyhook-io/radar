package relations

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestTraefikFamilies(t *testing.T) {
	cases := []struct {
		family                                 *Family
		kind, spec, targetKind, targetNS, path string
	}{
		{TraefikBackends, "IngressRoute", `{"routes":[{"services":[{"name":"target"}]}]}`, "Service", "shop", "spec.routes[].services[]"},
		{TraefikBackends, "IngressRouteTCP", `{"routes":[{"services":[{"name":"target","namespace":"shared"}]}]}`, "Service", "shared", "spec.routes[].services[]"},
		{TraefikBackends, "IngressRouteUDP", `{"routes":[{"services":[{"name":"target"}]}]}`, "Service", "shop", "spec.routes[].services[]"},
		{TraefikBackends, "IngressRoute", `{"routes":[{"services":[{"kind":"TraefikService","name":"target","namespace":"shared"}]}]}`, "TraefikService", "shared", "spec.routes[].services[]"},
		{TraefikBackends, "TraefikService", `{"weighted":{"services":[{"name":"target","namespace":"shared"}]}}`, "Service", "shared", "spec.weighted.services[]"},
		{TraefikBackends, "TraefikService", `{"highestRandomWeight":{"services":[{"kind":"TraefikService","name":"target"}]}}`, "TraefikService", "shop", "spec.highestRandomWeight.services[]"},
		{TraefikBackends, "TraefikService", `{"mirroring":{"name":"target"}}`, "Service", "shop", "spec.mirroring"},
		{TraefikBackends, "TraefikService", `{"mirroring":{"mirrors":[{"name":"target","namespace":"shared"}]}}`, "Service", "shared", "spec.mirroring.mirrors[]"},
		{TraefikRouteConfig, "IngressRoute", `{"routes":[{"middlewares":[{"name":"target","namespace":"shared"}]}]}`, "Middleware", "shared", "spec.routes[].middlewares[]"},
		{TraefikRouteConfig, "IngressRouteTCP", `{"routes":[{"middlewares":[{"name":"target"}]}]}`, "MiddlewareTCP", "shop", "spec.routes[].middlewares[]"},
		{TraefikRouteConfig, "Middleware", `{"chain":{"middlewares":[{"name":"target","namespace":"shared"}]}}`, "Middleware", "shared", "spec.chain.middlewares[]"},
		{TraefikRouteConfig, "IngressRoute", `{"routes":[{"services":[{"name":"backend","namespace":"shared","serversTransport":"target"}]}]}`, "ServersTransport", "shared", "spec.routes[].services[].serversTransport"},
		{TraefikRouteConfig, "IngressRouteTCP", `{"routes":[{"services":[{"name":"backend","namespace":"shared","serversTransport":"target"}]}]}`, "ServersTransportTCP", "shop", "spec.routes[].services[].serversTransport"},
		{TraefikRouteConfig, "IngressRoute", `{"tls":{"options":{"name":"target","namespace":"shared"},"store":{"name":"ignored"}}}`, "TLSOption", "shared", "spec.tls.options"},
		{TraefikRouteConfig, "IngressRouteTCP", `{"tls":{"options":{"name":"target"}}}`, "TLSOption", "shop", "spec.tls.options"},
		{TraefikSecrets, "ServersTransport", `{"rootCAs":[{"secret":"target"}]}`, "Secret", "shop", "spec.rootCAs[].secret"},
		{TraefikSecrets, "ServersTransport", `{"rootCAs":[{"configMap":"target"}]}`, "ConfigMap", "shop", "spec.rootCAs[].configMap"},
		{TraefikSecrets, "ServersTransport", `{"rootCAsSecrets":["target"]}`, "Secret", "shop", "spec.rootCAsSecrets[]"},
		{TraefikSecrets, "ServersTransport", `{"certificatesSecrets":["target"]}`, "Secret", "shop", "spec.certificatesSecrets[]"},
		{TraefikSecrets, "ServersTransportTCP", `{"tls":{"rootCAs":[{"secret":"target"}]}}`, "Secret", "shop", "spec.tls.rootCAs[].secret"},
		{TraefikSecrets, "ServersTransportTCP", `{"tls":{"rootCAs":[{"configMap":"target"}]}}`, "ConfigMap", "shop", "spec.tls.rootCAs[].configMap"},
		{TraefikSecrets, "ServersTransportTCP", `{"tls":{"rootCAsSecrets":["target"]}}`, "Secret", "shop", "spec.tls.rootCAsSecrets[]"},
		{TraefikSecrets, "ServersTransportTCP", `{"tls":{"certificatesSecrets":["target"]}}`, "Secret", "shop", "spec.tls.certificatesSecrets[]"},
		{TraefikSecrets, "TLSOption", `{"clientAuth":{"secretNames":["target"]}}`, "Secret", "shop", "spec.clientAuth.secretNames[]"},
		{TraefikSecrets, "TLSStore", `{"defaultCertificate":{"secretName":"target"}}`, "Secret", "shop", "spec.defaultCertificate.secretName"},
		{TraefikSecrets, "TLSStore", `{"certificates":[{"secretName":"target"}]}`, "Secret", "shop", "spec.certificates[].secretName"},
		{TraefikSecrets, "IngressRoute", `{"tls":{"secretName":"target"}}`, "Secret", "shop", "spec.tls.secretName"},
		{TraefikSecrets, "IngressRouteTCP", `{"tls":{"secretName":"target"}}`, "Secret", "shop", "spec.tls.secretName"},
	}
	for _, group := range []string{configrefs.TraefikGroup, configrefs.TraefikLegacyGroup} {
		for _, tc := range cases {
			t.Run(group+"/"+tc.kind+"/"+tc.path, func(t *testing.T) {
				var spec map[string]any
				if err := json.Unmarshal([]byte(tc.spec), &spec); err != nil {
					t.Fatal(err)
				}
				source := object(group+"/v1alpha1", tc.kind, "source", spec)
				apiVersion := group + "/v1alpha1"
				if tc.targetKind == "Service" || tc.targetKind == "Secret" || tc.targetKind == "ConfigMap" {
					apiVersion = "v1"
				}
				target := inNamespace(object(apiVersion, tc.targetKind, "target", nil), tc.targetNS)
				// A cluster serving both groups reads a legacy-only target
				// as legacy_group_only; this one predates traefik.io.
				var s Session = allNamespaces{cluster: cluster{objects: []runtime.Object{source, target}}}
				if group == configrefs.TraefikLegacyGroup {
					s = legacyTraefik{s.(allNamespaces)}
				}
				checkFamilyIn(t, s, tc.family, source, refOf(target), tc.path)
			})
		}
	}
}

func TestTraefikOtherBackendKindsAreNotServices(t *testing.T) {
	for _, kind := range []string{"IngressRoute", "IngressRouteTCP", "IngressRouteUDP", "TraefikService"} {
		backend := map[string]any{"kind": "Other", "name": "target", "serversTransport": "transport"}
		spec := map[string]any{"routes": []any{map[string]any{"services": []any{backend}}}}
		if kind == "TraefikService" {
			spec = map[string]any{"weighted": map[string]any{"services": []any{backend}}, "highestRandomWeight": map[string]any{"services": []any{backend}}, "mirroring": map[string]any{"kind": "Other", "name": "target", "mirrors": []any{backend}}}
		}
		source := object("traefik.io/v1alpha1", kind, "source", spec)
		target := object("v1", "Service", "target", nil)
		v := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{source, target}}}, ownership.Unrestricted())
		if got := v.References(TraefikBackends, refOf(source), source); len(got.Declared) != 0 {
			t.Fatalf("%s other kind declared: %+v", kind, got)
		}
		if got := v.Referrers(TraefikBackends, refOf(target), nil); len(got.Referrers) != 0 {
			t.Fatalf("%s other kind referred: %+v", kind, got)
		}
		if kind != "TraefikService" {
			if got := v.References(TraefikRouteConfig, refOf(source), source); len(got.Declared) != 0 {
				t.Fatalf("%s transport for other kind: %+v", kind, got)
			}
		}
	}
}

func TestTraefikCrossNamespaceAccessAndSourceNarrowing(t *testing.T) {
	source := object("traefik.io/v1alpha1", "IngressRoute", "route", map[string]any{"routes": []any{map[string]any{"middlewares": []any{map[string]any{"name": "config", "namespace": "shared"}}}}})
	target := inNamespace(object("traefik.io/v1alpha1", "Middleware", "config", nil), "shared")
	c := allNamespaces{cluster: cluster{objects: []runtime.Object{source, target}}}
	denied := NewView(c, namespaceAccess{"shared", nil}).Referrers(TraefikRouteConfig, refOf(target), nil)
	if len(denied.Referrers) != 0 || !denied.Coverage.Narrowed {
		t.Fatalf("unreadable source: %+v", denied)
	}
	got := NewView(c, namespaceAccess{"shop", nil}).References(TraefikRouteConfig, refOf(source), source)
	if len(got.Declared) != 1 || got.Declared[0].State != TargetWithheld {
		t.Fatalf("unreadable target: %+v", got)
	}
	c.unread = map[resourceid.GroupKind]readreason.Code{
		{Group: "traefik.io", Kind: "Middleware"}:               readreason.KindSyncPending,
		{Group: "traefik.io", Kind: "IngressRouteTCP"}:          readreason.KindSyncPending,
		{Group: "traefik.containo.us", Kind: "IngressRouteTCP"}: readreason.KindSyncPending,
	}
	st := object("traefik.io/v1alpha1", "ServersTransport", "transport", nil)
	c.objects = append(c.objects, st)
	if got := NewView(c, namespaceAccess{"shop", nil}).Referrers(TraefikRouteConfig, refOf(st), nil); !got.Coverage.Complete() {
		t.Fatalf("unrelated source kinds read: %+v", got)
	}
	c.unread[resourceid.GroupKind{Group: "traefik.io", Kind: "IngressRoute"}] = readreason.KindSyncPending
	if got := NewView(c, namespaceAccess{"shop", nil}).Referrers(TraefikRouteConfig, refOf(st), nil); got.Coverage.Complete() {
		t.Fatalf("readable source gap lost: %+v", got)
	}

	c.unread = map[resourceid.GroupKind]readreason.Code{{Group: "traefik.io", Kind: "TLSOption"}: readreason.KindSyncPending}
	cm := configMap("config")
	c.objects = append(c.objects, cm)
	if got := NewView(c, namespaceAccess{"shop", nil}).Referrers(TraefikSecrets, refOf(cm), nil); !got.Coverage.Complete() {
		t.Fatalf("Secret-only source read for ConfigMap: %+v", got)
	}
}

func TestContourFamilies(t *testing.T) {
	cases := []struct {
		family                           *Family
		spec, targetKind, targetNS, path string
	}{
		{ContourBackends, `{"routes":[{"services":[{"name":"target"}]}]}`, "Service", "shop", "spec.routes[].services[]"},
		{ContourBackends, `{"tcpproxy":{"services":[{"name":"target"}]}}`, "Service", "shop", "spec.tcpproxy.services[]"},
		{ContourBackends, `{"includes":[{"name":"target","namespace":"shared"}]}`, "HTTPProxy", "shared", "spec.includes[]"},
		{ContourBackends, `{"includes":[{"name":"target"}]}`, "HTTPProxy", "shop", "spec.includes[]"},
		{ContourSecrets, `{"virtualhost":{"tls":{"secretName":"shared/target"}}}`, "Secret", "shared", "spec.virtualhost.tls.secretName"},
		{ContourSecrets, `{"virtualhost":{"tls":{"clientValidation":{"caSecret":"shared/target"}}}}`, "Secret", "shared", "spec.virtualhost.tls.clientValidation.caSecret"},
		{ContourSecrets, `{"virtualhost":{"tls":{"clientValidation":{"crlSecret":"target"}}}}`, "Secret", "shop", "spec.virtualhost.tls.clientValidation.crlSecret"},
		{ContourSecrets, `{"routes":[{"services":[{"validation":{"caSecret":"shared/target"}}]}]}`, "Secret", "shared", "spec.routes[].services[].validation.caSecret"},
		{ContourSecrets, `{"tcpproxy":{"services":[{"validation":{"caSecret":"shared/target"}}]}}`, "Secret", "shared", "spec.tcpproxy.services[].validation.caSecret"},
		{ContourSecrets, `{"virtualhost":{"jwtProviders":[{"remoteJWKS":{"validation":{"caSecret":"shared/target"}}}]}}`, "Secret", "shared", "spec.virtualhost.jwtProviders[].remoteJWKS.validation.caSecret"},
	}
	for _, tc := range cases {
		t.Run(tc.path+"/"+tc.targetNS, func(t *testing.T) {
			var spec map[string]any
			if err := json.Unmarshal([]byte(tc.spec), &spec); err != nil {
				t.Fatal(err)
			}
			source := object("projectcontour.io/v1", "HTTPProxy", "source", spec)
			apiVersion := "v1"
			if tc.targetKind == "HTTPProxy" {
				apiVersion = "projectcontour.io/v1"
			}
			target := inNamespace(object(apiVersion, tc.targetKind, "target", nil), tc.targetNS)
			checkFamily(t, tc.family, []runtime.Object{source, target}, source, refOf(target), tc.path)
		})
	}
}

func TestRepeatedTraefikDeclarationsKeepEveryPath(t *testing.T) {
	source := object("traefik.io/v1alpha1", "TLSStore", "store", map[string]any{"defaultCertificate": map[string]any{"secretName": "tls"}, "certificates": []any{map[string]any{"secretName": "tls"}}})
	v := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{source, secret("tls")}}}, ownership.Unrestricted())
	want := []string{"spec.defaultCertificate.secretName", "spec.certificates[].secretName"}
	got := v.References(TraefikSecrets, refOf(source), source)
	if len(got.Declared) != 1 || !slices.Equal(got.Declared[0].Paths, want) {
		t.Fatalf("paths lost: %+v", got)
	}
	reverse := v.Referrers(TraefikSecrets, ref("", "Secret", "tls"), nil)
	if len(reverse.Referrers) != 1 || !slices.Equal(reverse.Referrers[0].Paths, want) {
		t.Fatalf("reverse paths lost: %+v", reverse)
	}
}

func TestTraefikSecretsStayInTheDeclarersNamespace(t *testing.T) {
	source := object("traefik.io/v1alpha1", "IngressRoute", "route", map[string]any{"tls": map[string]any{"secretName": "tls"}})
	local := object("v1", "Secret", "tls", nil)
	remote := inNamespace(object("v1", "Secret", "tls", nil), "shared")
	checkFamily(t, TraefikSecrets, []runtime.Object{source, local, remote}, source, refOf(local), "spec.tls.secretName")
	v := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{source, local, remote}}}, ownership.Unrestricted())
	if got := v.Referrers(TraefikSecrets, refOf(remote), nil); len(got.Referrers) != 0 {
		t.Fatalf("cross-namespace Secret joined: %+v", got)
	}
}

// Where both groups are served, Traefik 2.10 and 2.11 read a Middleware
// found only in the legacy group and Traefik 3 ignores it, so the reference
// names it with legacy_group_only rather than as present.
func TestATraefikReferenceFoundOnlyInTheLegacyGroupIsUnconfirmed(t *testing.T) {
	route := object("traefik.io/v1alpha1", "IngressRoute", "route", map[string]any{"routes": []any{map[string]any{"middlewares": []any{map[string]any{"name": "auth"}}}}})
	legacy := object("traefik.containo.us/v1alpha1", "Middleware", "auth", nil)
	gap := ownership.Gap{Kind: refOf(legacy).GroupKind(), Namespace: "shop", Reason: readreason.LegacyGroupOnly}
	v := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{route, legacy}}}, ownership.Unrestricted())
	refs := v.References(TraefikRouteConfig, refOf(route), route)
	if len(refs.Declared) != 1 || refs.Declared[0].Ref != refOf(legacy) || refs.Declared[0].State != TargetUnread || refs.Declared[0].Gap != gap || !slices.Contains(refs.Coverage.NotChecked, gap) {
		t.Errorf("a traefik.io route's Middleware only in traefik.containo.us reads as %+v", refs)
	}
	reverse := v.Referrers(TraefikRouteConfig, refOf(legacy), nil)
	if got := referrerRefs(reverse); len(got) != 1 || got[0] != refOf(route) || !slices.Contains(reverse.Coverage.NotChecked, gap) {
		t.Errorf("the legacy Middleware's referrers = %+v; want the traefik.io route, with the gap", reverse)
	}

	missing := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{route}}}, ownership.Unrestricted()).References(TraefikRouteConfig, refOf(route), route)
	if len(missing.Declared) != 1 || missing.Declared[0].State != TargetAbsent || missing.Declared[0].Ref.Group != "traefik.io" {
		t.Errorf("a Middleware in neither group reads as %+v; want absent, named in the route's group", missing.Declared)
	}
}

func TestTraefikPrefersItsCurrentGroupAndNeverFallsBackPastAnUnknown(t *testing.T) {
	route := object("traefik.containo.us/v1alpha1", "IngressRoute", "route", map[string]any{"routes": []any{map[string]any{"services": []any{map[string]any{"kind": "TraefikService", "name": "split"}}}}})
	current := object("traefik.io/v1alpha1", "TraefikService", "split", nil)
	shadowed := object("traefik.containo.us/v1alpha1", "TraefikService", "split", nil)
	c := allNamespaces{cluster: cluster{objects: []runtime.Object{route, current, shadowed}}}
	v := NewView(c, ownership.Unrestricted())
	if got := v.References(TraefikBackends, refOf(route), route); len(got.Declared) != 1 || got.Declared[0].Ref != refOf(current) {
		t.Errorf("with the name in both groups the route uses %+v; want traefik.io's", got.Declared)
	}
	if got := referrerRefs(v.Referrers(TraefikBackends, refOf(shadowed), nil)); len(got) != 0 {
		t.Errorf("the shadowed legacy TraefikService's referrers = %v; Traefik uses traefik.io's", got)
	}
	if got := referrerRefs(v.Referrers(TraefikBackends, refOf(current), nil)); len(got) != 1 {
		t.Errorf("traefik.io's TraefikService's referrers = %v; want the route", got)
	}

	c.unread = map[resourceid.GroupKind]readreason.Code{{Group: "traefik.io", Kind: "TraefikService"}: readreason.KindSyncPending}
	v = NewView(c, ownership.Unrestricted())
	if got := v.References(TraefikBackends, refOf(route), route); len(got.Declared) != 1 || got.Declared[0].State != TargetUnread || got.Coverage.Complete() {
		t.Errorf("with traefik.io's store loading the route's backend reads as %+v; want unread, not the legacy fallback", got)
	}
	if got := v.Referrers(TraefikBackends, refOf(shadowed), nil); len(got.Referrers) != 0 || got.Coverage.Complete() {
		t.Errorf("with traefik.io's store loading the legacy TraefikService's referrers are %+v; want none, incomplete", got)
	}
}

func TestAContourTCPProxyDelegates(t *testing.T) {
	child := inNamespace(object("projectcontour.io/v1", "HTTPProxy", "child", nil), "apps")
	parent := object("projectcontour.io/v1", "HTTPProxy", "parent", map[string]any{"tcpproxy": map[string]any{"include": map[string]any{"name": "child", "namespace": "apps"}}})
	checkFamily(t, ContourBackends, []runtime.Object{child, parent}, parent, resourceid.Ref{Group: "projectcontour.io", Kind: "HTTPProxy", Namespace: "apps", Name: "child"}, "spec.tcpproxy.include")
}

// legacyTraefik is a cluster whose discovery, complete, serves only the
// legacy Traefik group, as before Traefik 2.10.
type legacyTraefik struct{ allNamespaces }

func (legacyTraefik) Catalog() *ownership.Catalog {
	var served []ownership.ServedResource
	for _, kind := range []string{"IngressRoute", "IngressRouteTCP", "IngressRouteUDP", "TraefikService", "Middleware", "MiddlewareTCP", "ServersTransport", "ServersTransportTCP", "TLSOption", "TLSStore"} {
		served = append(served, ownership.ServedResource{Group: "traefik.containo.us", Version: "v1alpha1", Kind: kind, Resource: strings.ToLower(kind) + "s", Namespaced: true, Verbs: []string{"list", "watch", "get"}})
	}
	return ownership.NewCatalog(served, true)
}

func TestAnUnservedTraefikGroupShadowsNothing(t *testing.T) {
	route := object("traefik.containo.us/v1alpha1", "IngressRoute", "route", map[string]any{"routes": []any{map[string]any{"services": []any{map[string]any{"kind": "TraefikService", "name": "split"}}}}})
	split := object("traefik.containo.us/v1alpha1", "TraefikService", "split", nil)
	c := legacyTraefik{allNamespaces{cluster: cluster{objects: []runtime.Object{route, split}, unread: map[resourceid.GroupKind]readreason.Code{{Group: "traefik.io", Kind: "TraefikService"}: readreason.KindUnknown}}}}
	v := NewView(c, ownership.Unrestricted())
	if got := v.References(TraefikBackends, refOf(route), route); len(got.Declared) != 1 || got.Declared[0].Ref != refOf(split) || got.Declared[0].State != TargetPresent || !got.Coverage.Complete() {
		t.Errorf("on a legacy-only cluster the route's backend reads as %+v", got)
	}
	if got := v.Referrers(TraefikBackends, refOf(split), nil); len(got.Referrers) != 1 || !got.Coverage.Complete() {
		t.Errorf("on a legacy-only cluster the TraefikService's referrers are %+v; want the route", got)
	}
}

// ambiguousTraefik is a cluster whose complete discovery serves
// traefik.io's TraefikService through two resources, so Radar can't tell
// which one a reference means.
type ambiguousTraefik struct{ allNamespaces }

func (ambiguousTraefik) Catalog() *ownership.Catalog {
	served := []ownership.ServedResource{
		{Group: "traefik.containo.us", Version: "v1alpha1", Kind: "IngressRoute", Resource: "ingressroutes", Namespaced: true, Verbs: []string{"list", "watch", "get"}},
		{Group: "traefik.containo.us", Version: "v1alpha1", Kind: "TraefikService", Resource: "traefikservices", Namespaced: true, Verbs: []string{"list", "watch", "get"}},
		{Group: "traefik.io", Version: "v1alpha1", Kind: "TraefikService", Resource: "traefikservices", Namespaced: true, Verbs: []string{"list", "watch", "get"}},
		{Group: "traefik.io", Version: "v1alpha1", Kind: "TraefikService", Resource: "traefikservicesalt", Namespaced: true, Verbs: []string{"list", "watch", "get"}},
	}
	return ownership.NewCatalog(served, true)
}

func TestAnAmbiguousPreferredGroupStillStopsTheResolution(t *testing.T) {
	route := object("traefik.containo.us/v1alpha1", "IngressRoute", "route", map[string]any{"routes": []any{map[string]any{"services": []any{map[string]any{"kind": "TraefikService", "name": "split"}}}}})
	split := object("traefik.containo.us/v1alpha1", "TraefikService", "split", nil)
	c := ambiguousTraefik{allNamespaces{cluster: cluster{objects: []runtime.Object{route, split}, unread: map[resourceid.GroupKind]readreason.Code{{Group: "traefik.io", Kind: "TraefikService"}: readreason.ReadError}}}}
	if !c.Catalog().Ambiguous(resourceid.GroupKind{Group: "traefik.io", Kind: "TraefikService"}) {
		t.Fatal("the fixture's traefik.io TraefikService isn't ambiguous")
	}
	v := NewView(c, ownership.Unrestricted())
	if got := v.References(TraefikBackends, refOf(route), route); len(got.Declared) != 1 || got.Declared[0].State != TargetUnread {
		t.Errorf("with traefik.io's TraefikService ambiguous the route's backend reads as %+v; want unread, not the legacy one", got.Declared)
	}
	if got := v.Referrers(TraefikBackends, refOf(split), nil); len(got.Referrers) != 0 || got.Coverage.Complete() {
		t.Errorf("the legacy TraefikService's referrers are %+v; want none, incomplete", got)
	}
}

// legacyDenied may read everything but traefik.containo.us Middlewares,
// whose check it can't make when undecided is set.
type legacyDenied struct{ undecided bool }

func (a legacyDenied) CanRead(gk resourceid.GroupKind, _ string) ownership.Decision {
	if gk != (resourceid.GroupKind{Group: configrefs.TraefikLegacyGroup, Kind: "Middleware"}) {
		return ownership.Allowed
	}
	if a.undecided {
		return ownership.Undecided
	}
	return ownership.Denied
}
func (legacyDenied) Namespaces() ([]string, bool) { return nil, true }

func TestAResolutionStoppedAtALaterCandidateNamesIt(t *testing.T) {
	route := object("traefik.io/v1alpha1", "IngressRoute", "route", map[string]any{"routes": []any{map[string]any{"middlewares": []any{map[string]any{"name": "auth"}}}}})
	legacy := ref(configrefs.TraefikLegacyGroup, "Middleware", "auth")
	c := allNamespaces{cluster: cluster{objects: []runtime.Object{route}}}
	views := map[string]*View{
		"withheld":  NewView(c, legacyDenied{}),
		"undecided": NewView(c, legacyDenied{undecided: true}),
		"unread":    NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{route}, unread: map[resourceid.GroupKind]readreason.Code{legacy.GroupKind(): readreason.KindSyncPending}}}, ownership.Unrestricted()),
	}
	want := map[string]TargetState{"withheld": TargetWithheld, "undecided": TargetUnread, "unread": TargetUnread}
	for name, v := range views {
		got := v.References(TraefikRouteConfig, refOf(route), route)
		if len(got.Declared) != 1 || got.Declared[0].Ref != legacy || got.Declared[0].State != want[name] {
			t.Errorf("%s: with traefik.io's Middleware absent the route's Middleware reads as %+v; want %v, named in the legacy group", name, got.Declared, want[name])
		}
		if gap := got.Declared[0].Gap; want[name] == TargetUnread && gap.Kind != legacy.GroupKind() {
			t.Errorf("%s: gap %+v names a kind other than the Middleware it couldn't read", name, gap)
		}
	}
}

func TestAnAbsentTargetIsNamedInAServedGroup(t *testing.T) {
	route := object("traefik.containo.us/v1alpha1", "IngressRoute", "route", map[string]any{"routes": []any{map[string]any{"services": []any{map[string]any{"kind": "TraefikService", "name": "split"}}}}})
	v := NewView(legacyTraefik{allNamespaces{cluster: cluster{objects: []runtime.Object{route}}}}, ownership.Unrestricted())
	got := v.References(TraefikBackends, refOf(route), route)
	if len(got.Declared) != 1 || got.Declared[0].State != TargetAbsent || got.Declared[0].Ref != ref(configrefs.TraefikLegacyGroup, "TraefikService", "split") {
		t.Errorf("on a legacy-only cluster a missing TraefikService reads as %+v; want absent, named in the served legacy group", got.Declared)
	}
}

func TestTraefikReferrersReadOnlyRoutesThatCanDeclareTheTarget(t *testing.T) {
	loading := map[resourceid.GroupKind]readreason.Code{}
	for _, group := range []string{configrefs.TraefikGroup, configrefs.TraefikLegacyGroup} {
		loading[resourceid.GroupKind{Group: group, Kind: "IngressRouteUDP"}] = readreason.KindSyncPending
	}
	middleware := object("traefik.io/v1alpha1", "Middleware", "auth", nil)
	v := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{middleware}, unread: loading}}, namespaceAccess{"shop", nil})
	if got := v.Referrers(TraefikRouteConfig, refOf(middleware), nil); !got.Coverage.Complete() {
		t.Errorf("a Middleware's referrers read UDP routes, which have none: %+v", got.Coverage)
	}
	for _, group := range []string{configrefs.TraefikGroup, configrefs.TraefikLegacyGroup} {
		loading[resourceid.GroupKind{Group: group, Kind: "IngressRouteTCP"}] = readreason.KindSyncPending
	}
	split := object("traefik.io/v1alpha1", "TraefikService", "split", nil)
	v = NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{split}, unread: loading}}, namespaceAccess{"shop", nil})
	if got := v.Referrers(TraefikBackends, refOf(split), nil); !got.Coverage.Complete() {
		t.Errorf("a TraefikService's referrers read TCP and UDP routes, which send only to Services: %+v", got.Coverage)
	}
}
