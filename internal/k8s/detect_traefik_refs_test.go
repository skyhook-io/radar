package k8s

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/certs"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func traefikObject(kind, ns, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "traefik.io/v1alpha1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": ns, "creationTimestamp": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
		"spec":       spec,
	}}
}

func ingressRoute(ns, name string, extra map[string]any, services []any, middlewares []any) *unstructured.Unstructured {
	route := map[string]any{"kind": "Rule", "match": "Host(`" + name + ".example.test`)"}
	if services != nil {
		route["services"] = services
	}
	if middlewares != nil {
		route["middlewares"] = middlewares
	}
	spec := map[string]any{"routes": []any{route}}
	for k, v := range extra {
		spec[k] = v
	}
	return traefikObject("IngressRoute", ns, name, spec)
}

func svcRef(name string) []any { return []any{map[string]any{"name": name, "port": int64(80)}} }

func mwRef(names ...string) []any {
	out := []any{}
	for _, n := range names {
		out = append(out, map[string]any{"name": n})
	}
	return out
}

func traefikPod(ns string, ready bool, args ...string) *corev1.Pod {
	status := corev1.ConditionTrue
	if !ready {
		status = corev1.ConditionFalse
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "traefik-0", Namespace: ns, Labels: map[string]string{"app.kubernetes.io/name": "traefik"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "traefik", Image: "docker.io/traefik:v3.7.14", Args: args}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}},
	}
}

var traefikArgs = []string{
	"--entryPoints.web.address=:8000/tcp",
	"--entryPoints.websecure.address=:8443/tcp",
	"--providers.kubernetescrd",
	"--providers.kubernetescrd.namespaces=shop,edge",
	"--providers.kubernetescrd.allowCrossNamespace=true",
}

func initTraefikFixture(t *testing.T, pod *corev1.Pod, extra ...runtime.Object) {
	t.Helper()
	typed := []runtime.Object{
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "edge"}},
	}
	if pod != nil {
		typed = append(typed, pod)
	}
	if err := InitTestResourceCache(fake.NewClientset(typed...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	gvrs := map[schema.GroupVersionResource]string{
		{Group: "traefik.io", Version: "v1alpha1", Resource: "ingressroutes"}:   "IngressRouteList",
		{Group: "traefik.io", Version: "v1alpha1", Resource: "middlewares"}:     "MiddlewareList",
		{Group: "traefik.io", Version: "v1alpha1", Resource: "traefikservices"}: "TraefikServiceList",
	}
	objects := []runtime.Object{
		traefikObject("Middleware", "shop", "strip", map[string]any{"stripPrefix": map[string]any{"prefixes": []any{"/api"}}}),
		traefikObject("Middleware", "shop", "secure-chain", map[string]any{"chain": map[string]any{"middlewares": mwRef("strip", "gone-auth")}}),
		traefikObject("Middleware", "shop", "error-page", map[string]any{"errors": map[string]any{"status": []any{"500-599"}, "service": map[string]any{"name": "gone-errors", "port": int64(80)}}}),
		traefikObject("Middleware", "platform", "remote-chain", map[string]any{"chain": map[string]any{"middlewares": mwRef("gone-remote")}}),
		traefikObject("Middleware", "edge", "remote-errors", map[string]any{"errors": map[string]any{"status": []any{"500-599"}, "service": map[string]any{"name": "gone-remote-errors", "namespace": "shop", "port": int64(80)}}}),
		traefikObject("Middleware", "shop", "unused-chain", map[string]any{"chain": map[string]any{"middlewares": mwRef("never-there")}}),
		traefikObject("TraefikService", "shop", "weighted", map[string]any{"weighted": map[string]any{"services": svcRef("web")}}),
		ingressRoute("shop", "ok", nil, append(svcRef("web"), map[string]any{"name": "weighted", "kind": "TraefikService"}), mwRef("strip")),
		ingressRoute("shop", "missing-svc", nil, svcRef("gone"), nil),
		ingressRoute("shop", "missing-traefikservice", nil, []any{map[string]any{"name": "gone-weighted", "kind": "TraefikService"}}, nil),
		ingressRoute("shop", "missing-mw", nil, svcRef("web"), mwRef("gone-mw")),
		ingressRoute("shop", "via-chain", nil, svcRef("web"), mwRef("secure-chain")),
		ingressRoute("shop", "via-errors", nil, svcRef("web"), mwRef("error-page")),
		ingressRoute("shop", "file-provider-mw", nil, svcRef("web"), mwRef("auth@file")),
		// A generated identifier, not an object name: can't be looked up.
		ingressRoute("shop", "generated-name-mw", nil, svcRef("web"), mwRef("shop-strip@kubernetescrd")),
		// The chain lives in another namespace; reading the row reveals it.
		ingressRoute("edge", "via-remote-chain", nil, svcRef("web"), []any{map[string]any{"name": "remote-chain", "namespace": "platform"}}),
		// Not loaded by this Traefik: another class, an entry point it lacks,
		// a namespace outside its provider's list.
		ingressRoute("shop", "other-class", map[string]any{"ingressClassName": "internal"}, svcRef("gone"), nil),
		ingressRoute("shop", "other-entrypoint", map[string]any{"entryPoints": []any{"admin"}}, svcRef("gone"), nil),
		ingressRoute("elsewhere", "other-namespace", nil, svcRef("gone"), nil),
		ingressRoute("edge", "cross-namespace", nil, []any{map[string]any{"name": "gone", "namespace": "shop", "port": int64(80)}}, nil),
		ingressRoute("edge", "via-remote-errors", nil, svcRef("web"), mwRef("remote-errors")),
		ingressRoute("edge", "named-entrypoint", map[string]any{"entryPoints": []any{"websecure"}}, svcRef("gone"), nil),
		// Traefik serves the router on the entry point it has.
		ingressRoute("shop", "partial-entrypoints", map[string]any{"entryPoints": []any{"web", "admin"}}, svcRef("gone"), nil),
	}
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrs, append(objects, extra...)...)
	resources := []APIResource{
		{Group: "traefik.io", Version: "v1alpha1", Kind: "IngressRoute", Name: "ingressroutes", Verbs: []string{"list", "watch"}},
		{Group: "traefik.io", Version: "v1alpha1", Kind: "Middleware", Name: "middlewares", Verbs: []string{"list", "watch"}},
		{Group: "traefik.io", Version: "v1alpha1", Kind: "TraefikService", Name: "traefikservices", Verbs: []string{"list", "watch"}},
	}
	if err := InitTestDynamicResourceCache(dynClient, resources); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	dynCache := GetDynamicResourceCache()
	for gvr := range gvrs {
		if err := dynCache.EnsureWatching(gvr); err != nil {
			t.Fatalf("EnsureWatching %s: %v", gvr.Resource, err)
		}
	}
	for gvr := range gvrs {
		if !dynCache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("%s did not sync", gvr.Resource)
		}
	}
}

func TestDetectLiveTraefikMissingRefs(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initTraefikFixture(t, traefikPod("traefik", true, traefikArgs...))

	got := DetectLiveTraefikMissingRefs(GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery(), "", nil)
	byName := map[string]Detection{}
	for _, d := range got {
		if _, dup := byName[d.Name]; dup {
			t.Fatalf("two rows for %s: %+v", d.Name, got)
		}
		byName[d.Name] = d
	}
	want := map[string]struct{ reason, text string }{
		"missing-svc":            {TraefikRouterMissingServiceReason, `references Service "shop/gone", which does not exist`},
		"missing-traefikservice": {TraefikRouterMissingServiceReason, `references TraefikService "shop/gone-weighted"`},
		"missing-mw":             {TraefikRouterMissingMiddlewareReason, `references Middleware "shop/gone-mw"`},
		"via-chain":              {TraefikRouterMissingMiddlewareReason, `uses chain Middleware "shop/secure-chain", which references Middleware "shop/gone-auth"`},
		"via-errors":             {TraefikRouterMissingServiceReason, `uses errors Middleware "shop/error-page", which references Service "shop/gone-errors"`},
		"named-entrypoint":       {TraefikRouterMissingServiceReason, `references Service "edge/gone"`},
		"partial-entrypoints":    {TraefikRouterMissingServiceReason, `references Service "shop/gone"`},
		"via-remote-errors":      {TraefikRouterMissingServiceReason, `uses errors Middleware "edge/remote-errors", which references Service "shop/gone-remote-errors"`},
		"cross-namespace":        {TraefikRouterMissingServiceReason, `references Service "shop/gone"`},
		"via-remote-chain":       {TraefikRouterMissingMiddlewareReason, `uses chain Middleware "platform/remote-chain", which references Middleware "platform/gone-remote"`},
	}
	if len(byName) != len(want) {
		t.Fatalf("rows for %v, want %d rows", keys(byName), len(want))
	}
	for name, w := range want {
		d, ok := byName[name]
		if !ok {
			t.Fatalf("no row for %s; rows: %v", name, keys(byName))
		}
		if d.Kind != "IngressRoute" || d.Group != "traefik.io" || d.Severity != "critical" || d.Reason != w.reason || !strings.Contains(d.Message, w.text) {
			t.Errorf("%s: %+v", name, d)
		}
		if !strings.Contains(d.Cause, "does not load a router") || !d.OnsetUnknown {
			t.Errorf("%s cause/onset: %+v", name, d)
		}
		hasPods := false
		for _, r := range d.RequiredReads {
			hasPods = hasPods || r == (issuesapi.EvidenceRead{Resource: "pods", Namespace: "traefik", Verb: "list"})
		}
		if !hasPods || len(d.RequiredReads) < 2 {
			t.Errorf("%s must require the missing kind's inventory and the Traefik pods: %+v", name, d.RequiredReads)
		}
	}

	remote := byName["via-remote-chain"]
	if !hasRead(remote.RequiredReads, issuesapi.EvidenceRead{Group: "traefik.io", Resource: "middlewares", Namespace: "platform", Verb: "list"}) {
		t.Errorf("a row naming another namespace's chain must require reading it: %+v", remote.RequiredReads)
	}

	scoped := DetectLiveTraefikMissingRefs(GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery(), "edge", nil)
	if len(scoped) != 4 {
		t.Fatalf("namespace-scoped scan = %+v", scoped)
	}
}

// Without allowCrossNamespace Traefik refuses a reference into another
// namespace for its own reason; that isn't a missing object.
func TestDetectLiveTraefikMissingRefsRefusedCrossNamespace(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initTraefikFixture(t, traefikPod("traefik", true, traefikArgs[:len(traefikArgs)-1]...))
	for _, d := range DetectLiveTraefikMissingRefs(GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery(), "edge", nil) {
		if d.Name == "cross-namespace" || d.Name == "via-remote-chain" || d.Name == "via-remote-errors" {
			t.Errorf("cross-namespace reference reported as missing: %+v", d)
		}
	}
}

func hasRead(reads []issuesapi.EvidenceRead, want issuesapi.EvidenceRead) bool {
	for _, r := range reads {
		if r == want {
			return true
		}
	}
	return false
}

// Without a running Traefik whose flags show it loads IngressRoutes, nothing
// proves the routes are live.
func TestDetectLiveTraefikMissingRefsNeedsRunningTraefik(t *testing.T) {
	for name, pod := range map[string]*corev1.Pod{
		"no pod":           nil,
		"not ready":        traefikPod("traefik", false, traefikArgs...),
		"config from file": traefikPod("traefik", true, "--configFile=/etc/traefik/traefik.yaml"),
		"crd disabled":     traefikPod("traefik", true, "--providers.kubernetescrd=false", "--providers.kubernetesingress"),
		// The file, when given, replaces the flags.
		"config file plus flags": traefikPod("traefik", true, append([]string{"--configFile=/etc/traefik/traefik.yaml"}, traefikArgs...)...),
		"remote cluster":         traefikPod("traefik", true, append([]string{"--providers.kubernetescrd.endpoint=https://other.example.test"}, traefikArgs...)...),
		"v2 image":               withImage(traefikPod("traefik", true, traefikArgs...), "traefik:v2.11.0"),
		"default config mounted": withConfigMount(traefikPod("traefik", true, traefikArgs...)),
	} {
		t.Run(name, func(t *testing.T) {
			defer ResetTestState()
			defer ResetTestDynamicState()
			initTraefikFixture(t, pod)
			if got := DetectLiveTraefikMissingRefs(GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery(), "", nil); len(got) != 0 {
				t.Fatalf("rows without a live Traefik: %+v", got)
			}
		})
	}
}

func withImage(pod *corev1.Pod, image string) *corev1.Pod {
	pod.Spec.Containers[0].Image = image
	return pod
}

func withConfigMount(pod *corev1.Pod) *corev1.Pod {
	pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "config", MountPath: "/etc/traefik"}}
	return pod
}

func TestParseTraefikFlags(t *testing.T) {
	parse := func(args ...string) (traefikInstance, bool) {
		return parseTraefikFlags(corev1.Container{Image: "traefik:v3.7.14", Args: args})
	}
	if _, ok := parse("--providers.kubernetescrd=false", "--providers.kubernetescrd.namespaces=shop"); ok {
		t.Error("a sub-option re-enabled an explicitly disabled provider")
	}
	inst, ok := parse("--providers.kubernetescrd.namespaces", "shop", "--providers.kubernetescrd.ingressclass", "internal")
	if !ok || !inst.namespaces["shop"] || len(inst.namespaces) != 1 || inst.ingressClass != "internal" {
		t.Errorf("space-separated values = %+v ok=%v", inst, ok)
	}
	if _, ok := parse("--providers.kubernetescrd", "--configFile", "/etc/traefik.yaml"); ok {
		t.Error("a space-separated config file was ignored")
	}
}

// An empty namespaces flag means every namespace, as in Traefik.
func TestParseTraefikFlagsEmptyNamespaces(t *testing.T) {
	inst, ok := parseTraefikFlags(corev1.Container{Image: "traefik:v3.7.14", Args: []string{"--providers.kubernetescrd.namespaces="}})
	if !ok || inst.namespaces != nil {
		t.Fatalf("instance = %+v ok=%v, want every namespace", inst, ok)
	}
}

func TestDetectLiveTraefikMissingRefsGeneratedNameCollisions(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initTraefikFixture(t, traefikPod("traefik", true, "--providers.kubernetescrd"),
		traefikObject("Middleware", "a", "b-c", map[string]any{"stripPrefix": map[string]any{"prefixes": []any{"/api"}}}),
		traefikObject("TraefikService", "a", "b-c", map[string]any{"weighted": map[string]any{"services": svcRef("web")}}),
		traefikObject("Middleware", "a-b", "auth.v2", map[string]any{}),
		traefikObject("TraefikService", "a-b", "auth.v2", map[string]any{}),
		traefikObject("Middleware", "a-b", "auth-v3", map[string]any{}),
		traefikObject("TraefikService", "a-b", "auth-v3", map[string]any{}),
		ingressRoute("a-b", "middleware-collision", nil, nil, mwRef("c")),
		ingressRoute("a-b", "service-collision", nil, []any{map[string]any{"name": "c", "kind": "TraefikService"}}, nil),
		ingressRoute("a-b", "middleware-normalized-object", nil, nil, mwRef("auth-v2")),
		ingressRoute("a-b", "service-normalized-object", nil, []any{map[string]any{"name": "auth-v2", "kind": "TraefikService"}}, nil),
		ingressRoute("a-b", "middleware-normalized-ref", nil, nil, mwRef("auth.v3", "auth--v3")),
		ingressRoute("a-b", "service-normalized-ref", nil, []any{map[string]any{"name": "auth.v3", "kind": "TraefikService"}, map[string]any{"name": "auth--v3", "kind": "TraefikService"}}, nil),
		ingressRoute("a-b", "middleware-missing", nil, nil, mwRef("d")),
		ingressRoute("a-b", "service-missing", nil, []any{map[string]any{"name": "d", "kind": "TraefikService"}}, nil),
	)
	got := DetectLiveTraefikMissingRefs(GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery(), "a-b", nil)
	byName := map[string]Detection{}
	for _, d := range got {
		byName[d.Name] = d
	}
	if len(got) != 2 || byName["middleware-missing"].Reason != TraefikRouterMissingMiddlewareReason || byName["service-missing"].Reason != TraefikRouterMissingServiceReason {
		t.Fatalf("generated-name collisions must not be reported missing: %+v", got)
	}
}

func TestDetectLiveTraefikMissingRefsGeneratedErrorServices(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initTraefikFixture(t, traefikPod("traefik", true, "--providers.kubernetescrd", "--providers.kubernetescrd.allowcrossnamespace"),
		traefikObject("Middleware", "generated", "errors", map[string]any{"errors": map[string]any{"service": map[string]any{"name": "web", "namespace": "shop", "port": int64(80)}}}),
		ingressRoute("generated", "errors-service", nil, []any{map[string]any{"name": "errors-errorpage-service", "kind": "TraefikService"}}, nil),
		ingressRoute("generated", "possible-errors-service", nil, []any{map[string]any{"name": "unknown-errorpage-service", "kind": "TraefikService"}}, nil),
		ingressRoute("generated", "normalized-errors-service", nil, []any{map[string]any{"name": "errors-errorpage.service", "kind": "TraefikService"}}, nil),
		ingressRoute("generated", "ordinary-traefikservice", nil, []any{map[string]any{"name": "missing", "kind": "TraefikService"}}, nil),
		ingressRoute("generated", "ordinary-service", nil, svcRef("errors-errorpage-service"), nil),
	)
	got := DetectLiveTraefikMissingRefs(GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery(), "generated", nil)
	byName := map[string]Detection{}
	for _, d := range got {
		byName[d.Name] = d
	}
	if len(got) != 2 || byName["ordinary-traefikservice"].Reason != TraefikRouterMissingServiceReason || byName["ordinary-service"].Reason != TraefikRouterMissingServiceReason {
		t.Fatalf("generated error services must not be reported missing: %+v", got)
	}
}

func TestDetectLiveTraefikMissingRefsAmbiguousMiddlewareTraversal(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initTraefikFixture(t, traefikPod("traefik", true, "--providers.kubernetescrd"),
		traefikObject("Middleware", "a", "b-chain", map[string]any{"chain": map[string]any{"middlewares": mwRef("gone")}}),
		traefikObject("Middleware", "a-b", "chain", map[string]any{"stripPrefix": map[string]any{"prefixes": []any{"/api"}}}),
		traefikObject("Middleware", "a", "b-errors", map[string]any{"errors": map[string]any{"service": map[string]any{"name": "gone", "port": int64(80)}}}),
		traefikObject("Middleware", "a-b", "errors", map[string]any{"stripPrefix": map[string]any{"prefixes": []any{"/api"}}}),
		traefikObject("Middleware", "a", "outer", map[string]any{"chain": map[string]any{"middlewares": mwRef("b-chain", "b-errors")}}),
		traefikObject("Middleware", "a", "unique", map[string]any{"chain": map[string]any{"middlewares": mwRef("gone")}}),
		traefikObject("TraefikService", "a", "b-weighted", map[string]any{"weighted": map[string]any{"services": svcRef("gone")}}),
		traefikObject("TraefikService", "a-b", "weighted", map[string]any{"weighted": map[string]any{"services": svcRef("web")}}),
		ingressRoute("a", "ambiguous-chain", nil, nil, mwRef("b-chain")),
		ingressRoute("a", "ambiguous-errors", nil, nil, mwRef("b-errors")),
		ingressRoute("a", "nested-ambiguous-chain", nil, nil, mwRef("outer")),
		ingressRoute("a", "ambiguous-service", nil, []any{map[string]any{"name": "b-weighted", "kind": "TraefikService"}}, nil),
		ingressRoute("a", "unique-chain", nil, nil, mwRef("unique")),
	)
	got := DetectLiveTraefikMissingRefs(GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery(), "a", nil)
	if len(got) != 1 || got[0].Name != "unique-chain" || got[0].Reason != TraefikRouterMissingMiddlewareReason {
		t.Fatalf("ambiguous configurations must not be traversed: %+v", got)
	}
}

func TestDetectLiveTraefikMissingRefsWorkingDirConfig(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	pod := traefikPod("traefik", true, traefikArgs...)
	pod.Spec.Containers[0].WorkingDir = "/config"
	pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "config", MountPath: "/config"}}
	initTraefikFixture(t, pod)
	if got := DetectLiveTraefikMissingRefs(GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery(), "", nil); len(got) != 0 {
		t.Fatalf("a mounted working directory may provide file configuration: %+v", got)
	}
}

func TestParseTraefikFlagsAutoLoadedConfig(t *testing.T) {
	for _, tt := range []struct {
		name, mountPath, workingDir string
		env                         []corev1.EnvVar
		want                        bool
	}{
		{name: "etc directory", mountPath: "/etc/traefik/"},
		{name: "etc ancestor", mountPath: "/etc"},
		{name: "root ancestor", mountPath: "/"},
		{name: "etc toml", mountPath: "/etc/traefik/traefik.toml"},
		{name: "etc yaml", mountPath: "/etc/traefik/traefik.yaml"},
		{name: "etc yml", mountPath: "/etc/traefik/traefik.yml"},
		{name: "working directory", mountPath: "/config", workingDir: "/config"},
		{name: "working directory ancestor", mountPath: "/config", workingDir: "/config/nested"},
		{name: "working directory file", mountPath: "/config/traefik.yml", workingDir: "/config"},
		{name: "xdg directory", mountPath: "/config", env: []corev1.EnvVar{{Name: "XDG_CONFIG_HOME", Value: "/config"}}},
		{name: "xdg ancestor", mountPath: "/config", env: []corev1.EnvVar{{Name: "XDG_CONFIG_HOME", Value: "/config/nested"}}},
		{name: "xdg file", mountPath: "/config/traefik.toml", env: []corev1.EnvVar{{Name: "XDG_CONFIG_HOME", Value: "/config"}}},
		{name: "home directory", mountPath: "/custom/.config", env: []corev1.EnvVar{{Name: "HOME", Value: "/custom"}}},
		{name: "home ancestor", mountPath: "/custom", env: []corev1.EnvVar{{Name: "HOME", Value: "/custom"}}},
		{name: "home file", mountPath: "/custom/.config/traefik.yaml", env: []corev1.EnvVar{{Name: "HOME", Value: "/custom"}}},
		{name: "unknown home", mountPath: "/config", env: []corev1.EnvVar{{Name: "HOME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}}}},
		{name: "expanded xdg", mountPath: "/config", env: []corev1.EnvVar{{Name: "XDG_CONFIG_HOME", Value: "$(CONFIG_DIR)"}}},
		{name: "unrelated directory", mountPath: "/config", want: true},
		{name: "shared path prefix", mountPath: "/etc/traef", want: true},
		{name: "nested file", mountPath: "/config/other/traefik.yaml", workingDir: "/config", want: true},
		{name: "unrelated extension", mountPath: "/etc/traefik/traefik.txt", want: true},
		{name: "root home not declared", mountPath: "/root/.config", want: true},
		{name: "traefik home not declared", mountPath: "/home/traefik/.config", want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := parseTraefikFlags(corev1.Container{
				Image: "traefik:v3.7.14", Args: traefikArgs, WorkingDir: tt.workingDir, Env: tt.env,
				VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: tt.mountPath}},
			})
			if ok != tt.want {
				t.Fatalf("configured from flags = %v, want %v", ok, tt.want)
			}
		})
	}
}

func TestParseTraefikFlagsEnvFrom(t *testing.T) {
	for name, source := range map[string]corev1.EnvFromSource{
		"config map": {ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "traefik-config"}}},
		"secret":     {SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "traefik-config"}}},
		"prefixed":   {Prefix: "TRAEFIK_", ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "static-config"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if inst, ok := parseTraefikFlags(corev1.Container{Image: "traefik:v3.7.14", Args: traefikArgs, EnvFrom: []corev1.EnvFromSource{source}}); ok {
				t.Fatalf("envFrom may supply unseen static configuration: %+v", inst)
			}
		})
	}
}

func TestTraefikLoadersEntryPoints(t *testing.T) {
	for _, tt := range []struct {
		name        string
		args        []string
		entryPoints []any
		want        bool
	}{
		{name: "default http", entryPoints: []any{"http"}, want: true},
		{name: "default unnamed", want: true},
		{name: "default rejects web", entryPoints: []any{"web"}},
		{name: "tcp", args: []string{"--entrypoints.web.address=:80/tcp"}, entryPoints: []any{"web"}, want: true},
		{name: "tcp unnamed", args: []string{"--entrypoints.web.address=:80"}, want: true},
		{name: "explicit replaces default", args: []string{"--entrypoints.web.address=:80"}, entryPoints: []any{"http"}},
		{name: "udp", args: []string{"--entrypoints.dns.address=:53/udp"}, entryPoints: []any{"dns"}},
		{name: "udp uppercase", args: []string{"--entrypoints.dns.address=:53/UDP"}, entryPoints: []any{"dns"}},
		{name: "udp unnamed", args: []string{"--entrypoints.dns.address=:53/udp"}},
		{name: "udp space separated", args: []string{"--entrypoints.dns.address", ":53/udp"}, entryPoints: []any{"dns"}},
		{name: "mixed udp route", args: []string{"--entrypoints.dns.address=:53/udp", "--entrypoints.web.address=:80"}, entryPoints: []any{"dns"}},
		{name: "mixed unnamed", args: []string{"--entrypoints.dns.address=:53/udp", "--entrypoints.web.address=:80"}, want: true},
		{name: "mixed route", args: []string{"--entrypoints.dns.address=:53/udp", "--entrypoints.web.address=:80"}, entryPoints: []any{"dns", "web"}, want: true},
		{name: "entry point options suppress default", args: []string{"--entrypoints.web.http.tls=true"}, entryPoints: []any{"http"}},
		{name: "internal unnamed", args: []string{"--entrypoints.traefik.address=:8080"}},
		{name: "internal explicit", args: []string{"--entrypoints.traefik.address=:8080"}, entryPoints: []any{"traefik"}, want: true},
		{name: "internal default excludes web", args: []string{"--entrypoints.traefik.address=:8080", "--entrypoints.traefik.asdefault=true", "--entrypoints.web.address=:80"}},
		{name: "udp default excludes web", args: []string{"--entrypoints.dns.address=:53/udp", "--entrypoints.dns.asdefault=true", "--entrypoints.web.address=:80"}},
		{name: "http default", args: []string{"--entrypoints.web.address=:80", "--entrypoints.web.asdefault=true", "--entrypoints.traefik.address=:8080"}, want: true},
		{name: "http default bare flag", args: []string{"--entrypoints.web.asdefault", "--entrypoints.web.address=:80"}, want: true},
		{name: "default false", args: []string{"--entrypoints.web.address=:80", "--entrypoints.web.asdefault=false"}, want: true},
		{name: "last default flag wins", args: []string{"--entrypoints.dns.address=:53/udp", "--entrypoints.dns.asdefault=true", "--entrypoints.dns.asdefault=false", "--entrypoints.web.address=:80"}, want: true},
		{name: "explicit nondefault", args: []string{"--entrypoints.web.address=:80", "--entrypoints.traefik.address=:8080", "--entrypoints.traefik.asdefault=true"}, entryPoints: []any{"web"}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inst, ok := parseTraefikFlags(corev1.Container{Image: "traefik:v3.7.14", Args: append([]string{"--providers.kubernetescrd"}, tt.args...)})
			if !ok {
				t.Fatal("expected a Traefik instance")
			}
			extra := map[string]any{}
			if tt.entryPoints != nil {
				extra["entryPoints"] = tt.entryPoints
			}
			route := ingressRoute("shop", "test", extra, svcRef("gone"), nil)
			if got := len(traefikLoaders([]traefikInstance{inst}, route)) > 0; got != tt.want {
				t.Fatalf("route admitted = %v, want %v; instance: %+v", got, tt.want, inst)
			}
		})
	}
}

func TestTraefikLoadersClassPresence(t *testing.T) {
	for _, tt := range []struct {
		name            string
		class           any
		annotations     map[string]string
		controllerClass string
		want            bool
	}{
		{name: "neither", want: true},
		{name: "field only", class: "internal", controllerClass: "internal", want: true},
		{name: "annotation only", annotations: map[string]string{"kubernetes.io/ingress.class": "internal"}, controllerClass: "internal", want: true},
		{name: "agree", class: "internal", annotations: map[string]string{"kubernetes.io/ingress.class": "internal"}, controllerClass: "internal", want: true},
		{name: "disagree", class: "internal", annotations: map[string]string{"kubernetes.io/ingress.class": "traefik"}, controllerClass: "internal"},
		{name: "empty field disagrees", class: "", annotations: map[string]string{"kubernetes.io/ingress.class": "traefik"}},
		{name: "empty annotation disagrees", class: "internal", annotations: map[string]string{"kubernetes.io/ingress.class": ""}, controllerClass: "internal"},
		{name: "empty field only", class: "", want: true},
		{name: "empty classes agree", class: "", annotations: map[string]string{"kubernetes.io/ingress.class": ""}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			extra := map[string]any{}
			if tt.class != nil {
				extra["ingressClassName"] = tt.class
			}
			route := ingressRoute("shop", "test", extra, svcRef("gone"), nil)
			route.SetAnnotations(tt.annotations)
			inst := traefikInstance{ingressClass: tt.controllerClass, entryPoints: map[string]bool{"http": true}, defaultEntryPoints: map[string]bool{"http": true}}
			if got := len(traefikLoaders([]traefikInstance{inst}, route)) > 0; got != tt.want {
				t.Fatalf("route admitted = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectLiveTraefikMissingRefsShadowedRouter(t *testing.T) {
	for _, tt := range []struct {
		name           string
		candidateExtra map[string]any
		brokenExtra    map[string]any
		services       []any
		middlewares    []any
		match          string
		extraArgs      []string
		deleted        bool
		wantWarning    bool
	}{
		{name: "identical rule", wantWarning: true},
		{name: "overlapping entry points", brokenExtra: map[string]any{"entryPoints": []any{"web", "websecure"}}, candidateExtra: map[string]any{"entryPoints": []any{"websecure"}}, wantWarning: true},
		{name: "disjoint entry points", brokenExtra: map[string]any{"entryPoints": []any{"web"}}, candidateExtra: map[string]any{"entryPoints": []any{"websecure"}}},
		{name: "byte different rule", match: "Host( `affected.example.test` )"},
		{name: "candidate missing service", services: svcRef("gone-too")},
		{name: "candidate missing middleware", middlewares: mwRef("gone-too")},
		{name: "candidate missing chain member", middlewares: mwRef("secure-chain")},
		{name: "candidate different class", candidateExtra: map[string]any{"ingressClassName": "internal"}},
		{name: "candidate namespace excluded", extraArgs: []string{"--providers.kubernetescrd.namespaces=edge"}},
		{name: "candidate deleted", deleted: true},
		{name: "default overlaps explicit", brokenExtra: map[string]any{"entryPoints": []any{"websecure"}}, extraArgs: []string{"--entrypoints.websecure.asdefault=true"}, wantWarning: true},
		{name: "default excludes nondefault", brokenExtra: map[string]any{"entryPoints": []any{"web"}}, extraArgs: []string{"--entrypoints.websecure.asdefault=true"}},
		{name: "default excludes internal", brokenExtra: map[string]any{"entryPoints": []any{"traefik"}}, extraArgs: []string{"--entrypoints.traefik.address=:8080"}},
		{name: "no defaults from internal", brokenExtra: map[string]any{"entryPoints": []any{"web"}}, extraArgs: []string{"--entrypoints.traefik.address=:8080", "--entrypoints.traefik.asdefault=true"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			defer ResetTestState()
			defer ResetTestDynamicState()
			services := tt.services
			if services == nil {
				services = svcRef("web")
			}
			candidate := ingressRoute("shop", "alternate", tt.candidateExtra, nil, nil)
			match := tt.match
			if match == "" {
				match = "Host(`affected.example.test`)"
			}
			if err := unstructured.SetNestedSlice(candidate.Object, []any{map[string]any{"kind": "Rule", "match": match, "services": services, "middlewares": tt.middlewares}}, "spec", "routes"); err != nil {
				t.Fatal(err)
			}
			if tt.deleted {
				now := metav1.Now()
				candidate.SetDeletionTimestamp(&now)
			}
			args := append([]string{"--providers.kubernetescrd", "--entrypoints.web.address=:80", "--entrypoints.websecure.address=:443"}, tt.extraArgs...)
			initTraefikFixture(t, traefikPod("traefik", true, args...), candidate,
				ingressRoute("edge", "affected", tt.brokenExtra, svcRef("gone"), nil),
			)
			got := DetectLiveTraefikMissingRefs(GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery(), "edge", nil)
			var affected []Detection
			for _, d := range got {
				if d.Name == "affected" {
					affected = append(affected, d)
				}
			}
			if len(affected) != 1 {
				t.Fatalf("affected rows = %+v", affected)
			}
			d := affected[0]
			if (d.Severity == "warning") != tt.wantWarning || strings.Contains(d.Cause, "Another route with the same rule may serve these requests.") != tt.wantWarning {
				t.Fatalf("shadowed = %v; detection = %+v", tt.wantWarning, d)
			}
			if tt.wantWarning && !hasRead(d.RequiredReads, issuesapi.EvidenceRead{Group: traefikGroup, Resource: "ingressroutes", Namespace: "shop", Verb: "list"}) {
				t.Fatalf("cross-namespace shadow evidence must require reading its route: %+v", d.RequiredReads)
			}
		})
	}
}

func TestDetectLiveTraefikMissingRefsShadowedRuleKeepsOtherFailureCritical(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	broken := ingressRoute("edge", "affected", nil, nil, nil)
	if err := unstructured.SetNestedSlice(broken.Object, []any{
		map[string]any{"match": "Host(`shared.example.test`)", "services": svcRef("gone")},
		map[string]any{"match": "Host(`unserved.example.test`)", "services": svcRef("gone")},
	}, "spec", "routes"); err != nil {
		t.Fatal(err)
	}
	candidate := ingressRoute("shop", "alternate", nil, nil, nil)
	if err := unstructured.SetNestedSlice(candidate.Object, []any{
		map[string]any{"match": "Host(`shared.example.test`)", "services": svcRef("web")},
	}, "spec", "routes"); err != nil {
		t.Fatal(err)
	}
	initTraefikFixture(t, traefikPod("traefik", true, "--providers.kubernetescrd"), broken, candidate)
	for _, d := range DetectLiveTraefikMissingRefs(GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery(), "edge", nil) {
		if d.Name == "affected" {
			if d.Severity != "critical" || !strings.Contains(d.Message, "unserved.example.test") || strings.Contains(d.Cause, "Another route with the same rule") {
				t.Fatalf("the unshadowed failure must remain critical: %+v", d)
			}
			return
		}
	}
	t.Fatal("missing affected row")
}

// A compose's scope loads each Traefik inventory once, and only when a route
// needs it.
func TestTraefikInventoryLoadsLazilyPerScope(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initTraefikFixture(t, traefikPod("traefik", true, "--providers.kubernetescrd"),
		ingressRoute("core-only", "core-missing", nil, svcRef("gone"), nil),
		ingressRoute("middleware-only", "middleware-missing", nil, nil, mwRef("gone")),
		ingressRoute("traefikservice-only", "traefikservice-missing", nil, []any{map[string]any{"name": "gone", "kind": "TraefikService"}}, nil),
	)
	cache, dynamicCache, discovery := GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery()
	scope := NewDetectScope()
	if got := DetectLiveTraefikMissingRefs(cache, dynamicCache, discovery, "core-only", scope); len(got) != 1 {
		t.Fatalf("core Service scan = %+v", got)
	}
	if len(scope.values) != 2 {
		t.Fatalf("core Service scan must load only instances and shadowing routes: %v", scope.values)
	}
	if got := DetectLiveTraefikMissingRefs(cache, dynamicCache, discovery, "middleware-only", scope); len(got) != 1 {
		t.Fatalf("Middleware scan = %+v", got)
	}
	if len(scope.values) != 3 {
		t.Fatalf("Middleware scan must load only its own inventory: %v", scope.values)
	}
	inv := newTraefikInventory(cache, dynamicCache, discovery, scope, traefikGroup)
	middlewares := inv.load("Middleware")
	if got := DetectLiveTraefikMissingRefs(cache, dynamicCache, discovery, "traefikservice-only", scope); len(got) != 1 {
		t.Fatalf("TraefikService scan = %+v", got)
	}
	services := inv.load("TraefikService")
	if len(scope.values) != 4 || middlewares == nil || services == nil {
		t.Fatalf("both inventories should now be loaded: %v", scope.values)
	}
	next := newTraefikInventory(cache, dynamicCache, discovery, scope, traefikGroup)
	for kind, want := range map[string]*traefikKindInventory{"Middleware": middlewares, "TraefikService": services} {
		if next.load(kind) != want {
			t.Errorf("%s inventory was rebuilt for the next namespace scan", kind)
		}
	}
	fresh := newTraefikInventory(cache, dynamicCache, discovery, NewDetectScope(), traefikGroup)
	if fresh.load("Middleware") == middlewares {
		t.Error("another compose reused this compose's inventory")
	}
}

func TestTraefikMajorVersion(t *testing.T) {
	for image, want := range map[string]int{
		"traefik:v3.7.14":                         3,
		"docker.io/traefik:v2.11.0":               2,
		"rancher/mirrored-library-traefik:2.10.5": 2,
		"registry:5000/traefik:3.1@sha256:abc":    3,
		"traefik":                                 0,
		"registry:5000/traefik":                   0,
	} {
		if got := traefikMajorVersion(image); got != want {
			t.Errorf("traefikMajorVersion(%q) = %d, want %d", image, got, want)
		}
	}
}

func keys(m map[string]Detection) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// IngressRoute certificates join Traefik's pool as competitors for any host,
// and a pool whose IngressRoutes Radar can't fully list reports nothing.
func TestTraefikIngressRouteTLSJoinsPool(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initTraefikFixture(t, traefikPod("traefik", true, "--providers.kubernetescrd"),
		ingressRoute("shop", "tls-route", map[string]any{"tls": map[string]any{"secretName": "route-tls"}}, svcRef("web"), nil))
	idx := &tlsServingIndex{referenced: map[string]map[string]map[string]bool{}, uncertain: map[string]bool{}}
	addTraefikIngressRouteTLSReferences(idx)
	if !idx.referenced[traefikIngressController]["shop/route-tls"][""] || idx.uncertain[traefikIngressController] {
		t.Fatalf("index = %+v, want shop/route-tls referenced for any host", idx)
	}
	idx.uncertain[traefikIngressController] = true
	use := tlsUse{secretNamespace: "shop", secretName: "expired-tls", host: "shop.example.test", controller: traefikIngressController}
	cert := certs.Input{Domains: []string{"shop.example.test"}}
	if tlsUseSelectsCertificate(idx, use, cert, time.Now(), func(string, string) (certs.Input, bool, bool) { return certs.Input{}, false, true }) {
		t.Fatal("an uncertain pool selected a certificate")
	}
}
