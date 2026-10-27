package k8s

import (
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/skyhook-io/radar/internal/ingressstatus"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	TraefikRouterMissingServiceReason    = "Missing Traefik router Service"
	TraefikRouterMissingMiddlewareReason = "Missing Traefik router Middleware"
)

const traefikGroup = "traefik.io"

// traefikInstance is what a running Traefik pod's flags say its Kubernetes
// CRD provider loads.
type traefikInstance struct {
	podNamespace        string
	namespaces          map[string]bool // nil: every namespace
	ingressClass        string
	labelSelector       labels.Selector // nil: every IngressRoute
	entryPoints         map[string]bool // HTTP-capable entry points
	defaultEntryPoints  map[string]bool
	allowCrossNamespace bool
}

// DetectLiveTraefikMissingRefs reports traefik.io IngressRoutes a running
// Traefik v3 loads
// whose route references a Service, TraefikService or Middleware that does
// not exist, directly or through a chain or errors Middleware the route uses.
// Traefik does not load such a router at all: the route's rule serves nothing
// (a 404 unless another router matches) and the error appears only in
// Traefik's log and dashboard.
//
// The liveness evidence is a ready Traefik v3 pod whose flags enable the
// Kubernetes CRD provider for the local cluster and whose namespace, ingress
// class, label selector and entry point settings admit the IngressRoute.
// Traefik reads a configuration file instead of its flags when one is given
// or mounted at its default location, so such a pod, like one configured
// through its environment, gives no evidence and its routes are left to
// audit. Traefik v2 merges two API groups' Middlewares, which this doesn't
// model, so v2 is left to audit too. Absence is asserted only from a
// synced cluster-wide inventory of the target kind, and the caller must be
// able to list the Traefik pods and the target kind in the target namespace.
func DetectLiveTraefikMissingRefs(cache *ResourceCache, dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, namespace string, scope *DetectScope) []Detection {
	if cache == nil || dynamicCache == nil || discovery == nil {
		return nil
	}
	if scope == nil {
		scope = NewDetectScope()
	}
	routeGVR, ok := discovery.GetGVRWithGroup("IngressRoute", traefikGroup)
	if !ok {
		return nil
	}
	// A namespace-filtered compose calls this once per namespace, so the
	// cluster-wide pod scan and inventory wait until a route needs them.
	routes := listDynamicForMissingRefs(dynamicCache, routeGVR, namespace, "IngressRoute")
	if len(routes) == 0 {
		return nil
	}
	instances := scope.get("traefikInstances", func() any { return runningTraefikInstances(cache) }).([]traefikInstance)
	if len(instances) == 0 {
		return nil
	}
	now := time.Now()
	var inv *traefikInventory
	var out []Detection
	for _, route := range routes {
		if route.GetDeletionTimestamp() != nil {
			continue
		}
		loaders := traefikLoaders(instances, route)
		if len(loaders) == 0 {
			continue
		}
		if inv == nil {
			inv = newTraefikInventory(cache, dynamicCache, discovery, scope, traefikGroup)
		}
		out = append(out, traefikRouteMissingRefs(route, traefikGroup, loaders, inv, now)...)
	}
	return out
}

// runningTraefikInstances reads the CRD provider settings off every ready
// Traefik pod's flags.
func runningTraefikInstances(cache *ResourceCache) []traefikInstance {
	podLister := cache.Pods()
	podLabels := ingressstatus.ControllerPodLabels["traefik.io/ingress-controller"]
	if podLister == nil || len(podLabels) == 0 {
		return nil
	}
	pods, err := podLister.List(labels.SelectorFromSet(podLabels))
	if err != nil {
		return nil
	}
	var out []traefikInstance
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || !isPodReadyForProblem(pod) {
			continue
		}
		for _, c := range pod.Spec.Containers {
			if inst, ok := parseTraefikFlags(c); ok {
				inst.podNamespace = pod.Namespace
				out = append(out, inst)
			}
		}
	}
	return out
}

func parseTraefikFlags(c corev1.Container) (traefikInstance, bool) {
	if traefikMajorVersion(c.Image) != 3 || len(c.EnvFrom) > 0 {
		return traefikInstance{}, false
	}
	configDirs := []string{"/etc/traefik"}
	if c.WorkingDir != "" {
		configDirs = append(configDirs, c.WorkingDir)
	}
	for _, env := range c.Env {
		if env.Name != "XDG_CONFIG_HOME" && env.Name != "HOME" {
			continue
		}
		if env.ValueFrom != nil || strings.Contains(env.Value, "$(") {
			return traefikInstance{}, false
		}
		if env.Value != "" {
			dir := env.Value
			if env.Name == "HOME" {
				dir = path.Join(dir, ".config")
			}
			configDirs = append(configDirs, dir)
		}
	}
	for _, m := range c.VolumeMounts {
		mountPath := path.Clean(m.MountPath)
		for _, dir := range configDirs {
			dir = path.Clean(dir)
			if mountPath == dir || strings.HasPrefix(dir, strings.TrimSuffix(mountPath, "/")+"/") || mountPath == path.Join(dir, "traefik.toml") || mountPath == path.Join(dir, "traefik.yaml") || mountPath == path.Join(dir, "traefik.yml") {
				return traefikInstance{}, false
			}
		}
	}
	inst := traefikInstance{entryPoints: map[string]bool{}}
	asDefault := map[string]bool{}
	enabled, disabled := false, false
	entryPointsConfigured := false
	args := append(append([]string(nil), c.Command...), c.Args...)
	for i := 0; i < len(args); i++ {
		key, value, hasValue := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		key = strings.ToLower(key)
		// Valued options may also take their value as the next argument.
		entryPointAddress := strings.HasPrefix(key, "entrypoints.") && strings.HasSuffix(key, ".address")
		entryPointDefault := strings.HasPrefix(key, "entrypoints.") && strings.HasSuffix(key, ".asdefault")
		if !hasValue && (traefikValuedFlags[key] || entryPointAddress || entryPointDefault) && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			value, hasValue = args[i+1], true
			i++
		}
		switch {
		case key == "configfile", key == "providers.kubernetescrd.endpoint":
			return traefikInstance{}, false
		case key == "providers.kubernetescrd":
			disabled = hasValue && !strings.EqualFold(value, "true")
			enabled = !disabled
		case key == "providers.kubernetescrd.namespaces":
			enabled = true
			for _, ns := range strings.Split(value, ",") {
				if ns = strings.TrimSpace(ns); ns != "" {
					if inst.namespaces == nil {
						inst.namespaces = map[string]bool{}
					}
					inst.namespaces[ns] = true
				}
			}
		case key == "providers.kubernetescrd.ingressclass":
			enabled = true
			inst.ingressClass = value
		case key == "providers.kubernetescrd.labelselector":
			enabled = true
			sel, err := labels.Parse(value)
			if err != nil {
				return traefikInstance{}, false
			}
			inst.labelSelector = sel
		case key == "providers.kubernetescrd.allowcrossnamespace":
			enabled = true
			inst.allowCrossNamespace = !hasValue || strings.EqualFold(value, "true")
		case strings.HasPrefix(key, "providers.kubernetescrd."):
			enabled = true
		case strings.HasPrefix(key, "entrypoints."):
			entryPointsConfigured = true
			if entryPointAddress {
				name := strings.TrimSuffix(strings.TrimPrefix(key, "entrypoints."), ".address")
				delete(inst.entryPoints, name)
				if value != "" && !strings.HasSuffix(strings.ToLower(value), "/udp") {
					inst.entryPoints[name] = true
				}
			} else if entryPointDefault {
				name := strings.TrimSuffix(strings.TrimPrefix(key, "entrypoints."), ".asdefault")
				delete(asDefault, name)
				if !hasValue || strings.EqualFold(value, "true") {
					asDefault[name] = true
				}
			}
		}
	}
	if !entryPointsConfigured {
		inst.entryPoints["http"] = true
	}
	inst.defaultEntryPoints = map[string]bool{}
	for name := range inst.entryPoints {
		if name != "traefik" && (len(asDefault) == 0 || asDefault[name]) {
			inst.defaultEntryPoints[name] = true
		}
	}
	return inst, enabled && !disabled
}

var traefikValuedFlags = map[string]bool{
	"configfile":                            true,
	"providers.kubernetescrd.endpoint":      true,
	"providers.kubernetescrd.namespaces":    true,
	"providers.kubernetescrd.ingressclass":  true,
	"providers.kubernetescrd.labelselector": true,
}

// traefikMajorVersion reads the major version from an image tag, or 0.
func traefikMajorVersion(image string) int {
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon <= slash {
		return 0
	}
	tag := strings.TrimPrefix(image[colon+1:], "v")
	switch {
	case strings.HasPrefix(tag, "2."), tag == "2":
		return 2
	case strings.HasPrefix(tag, "3."), tag == "3":
		return 3
	}
	return 0
}

// traefikLoaders returns the instances that load route, applying the CRD
// provider's own filters: namespaces, label selector, ingress class (an
// unconfigured provider takes routes with no class or class "traefik"), and
// entry points the route names.
func traefikLoaders(instances []traefikInstance, route *unstructured.Unstructured) []traefikInstance {
	class, hasClass, _ := unstructured.NestedString(route.Object, "spec", "ingressClassName")
	legacyClass, hasLegacyClass := route.GetAnnotations()["kubernetes.io/ingress.class"]
	// Traefik versions disagree on which class source wins.
	if hasClass && hasLegacyClass && class != legacyClass {
		return nil
	}
	if !hasClass {
		class = legacyClass
	}
	var out []traefikInstance
	for _, inst := range instances {
		if inst.namespaces != nil && !inst.namespaces[route.GetNamespace()] {
			continue
		}
		if inst.labelSelector != nil && !inst.labelSelector.Matches(labels.Set(route.GetLabels())) {
			continue
		}
		if class != inst.ingressClass && (inst.ingressClass != "" || class != "traefik") {
			continue
		}
		if len(inst.routeEntryPoints(route)) > 0 {
			out = append(out, inst)
		}
	}
	return out
}

func (inst traefikInstance) routeEntryPoints(route *unstructured.Unstructured) map[string]bool {
	entryPoints, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "entryPoints")
	if len(entryPoints) == 0 {
		return inst.defaultEntryPoints
	}
	served := map[string]bool{}
	for _, ep := range entryPoints {
		if ep = strings.ToLower(ep); inst.entryPoints[ep] {
			served[ep] = true
		}
	}
	return served
}

// traefikInventory answers "does this target exist" only where it can be
// sure: Services from an informer covering the namespace, Traefik kinds from
// a synced cluster-wide informer.
type traefikInventory struct {
	cache        *ResourceCache
	dynamicCache *DynamicResourceCache
	discovery    *ResourceDiscovery
	scope        *DetectScope
	group        string
}

// These maps are shared across namespace scans and must remain read-only.
type traefikKindInventory struct {
	objects        map[string]*unstructured.Unstructured // ns/name
	generatedNames map[string]int                        // normalized ns-name counts
}

func newTraefikInventory(cache *ResourceCache, dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, scope *DetectScope, group string) *traefikInventory {
	return &traefikInventory{cache: cache, dynamicCache: dynamicCache, discovery: discovery, scope: scope, group: group}
}

func (inv *traefikInventory) load(kind string) *traefikKindInventory {
	return inv.scope.get("traefikInventory\x00"+inv.group+"\x00"+kind, func() any {
		gvr, ok := inv.discovery.GetGVRWithGroup(kind, inv.group)
		if !ok || !inv.dynamicCache.IsClusterWideSynced(gvr) {
			return (*traefikKindInventory)(nil)
		}
		items, err := inv.dynamicCache.ListWatched(gvr)
		if err != nil {
			return (*traefikKindInventory)(nil)
		}
		inventory := &traefikKindInventory{objects: map[string]*unstructured.Unstructured{}, generatedNames: map[string]int{}}
		for _, obj := range items {
			inventory.objects[obj.GetNamespace()+"/"+obj.GetName()] = obj
			inventory.generatedNames[traefikGeneratedName(obj.GetNamespace(), obj.GetName())]++
		}
		return inventory
	}).(*traefikKindInventory)
}

// serviceMissing reports a Service or TraefikService reference known to be
// absent, with the inventory a caller must read to see that.
func (inv *traefikInventory) serviceMissing(ref map[string]any, ns string) (string, issuesapi.EvidenceRead, bool) {
	name, _ := ref["name"].(string)
	if name == "" || strings.Contains(name, "@") {
		return "", issuesapi.EvidenceRead{}, false
	}
	if kind, _ := ref["kind"].(string); kind == "TraefikService" {
		// Errors Middlewares can populate this configuration name without
		// any TraefikService object, including through normalized aliases.
		if strings.HasSuffix(traefikGeneratedName(ns, name), "-errorpage-service") {
			return "", issuesapi.EvidenceRead{}, false
		}
		services := inv.load("TraefikService")
		if services == nil || services.objects[ns+"/"+name] != nil || services.generatedNames[traefikGeneratedName(ns, name)] > 0 {
			return "", issuesapi.EvidenceRead{}, false
		}
		return fmt.Sprintf("TraefikService %q", traefikRefLabel(ns, name)), issuesapi.EvidenceRead{Group: inv.group, Resource: "traefikservices", Namespace: ns, Verb: "list"}, true
	} else if kind != "" && kind != "Service" {
		return "", issuesapi.EvidenceRead{}, false
	}
	svcLister := inv.cache.Services()
	if svcLister == nil || !inv.cache.IsKindReady("services") {
		return "", issuesapi.EvidenceRead{}, false
	}
	if _, err := svcLister.Services(ns).Get(name); !refKnownMissing(inv.cache, "services", ns, err) {
		return "", issuesapi.EvidenceRead{}, false
	}
	return fmt.Sprintf("Service %q", traefikRefLabel(ns, name)), issuesapi.EvidenceRead{Resource: "services", Namespace: ns, Verb: "list"}, true
}

// traefikMissingRef is a missing target; via names the Middleware the route
// uses that leads to it, when the route doesn't name it directly.
type traefikMissingRef struct {
	reason, target, via string
	// reads are the inventories that show the target missing and the spec
	// of every Middleware traversed to reach it.
	reads []issuesapi.EvidenceRead
}

// middlewareMissing walks a route's Middleware reference: a missing
// Middleware, or a chain member or errors-page Service missing under one it
// uses, fails the router the same way.
func (inv *traefikInventory) middlewareMissing(ref map[string]any, ns string, allowCrossNamespace bool, visited map[string]bool) (traefikMissingRef, bool) {
	name, _ := ref["name"].(string)
	// A name with a provider suffix is a generated Traefik identifier, not
	// an object name, so it can't be looked up directly.
	if name == "" || strings.Contains(name, "@") {
		return traefikMissingRef{}, false
	}
	if refNs, _ := ref["namespace"].(string); refNs != "" {
		if refNs != ns && !allowCrossNamespace {
			return traefikMissingRef{}, false
		}
		ns = refNs
	}
	middlewares := inv.load("Middleware")
	if middlewares == nil {
		return traefikMissingRef{}, false
	}
	identifier := traefikGeneratedName(ns, name)
	// A failing Middleware may be skipped before collision handling, so
	// its object's spec does not prove which configuration Traefik uses.
	if middlewares.generatedNames[identifier] > 1 {
		return traefikMissingRef{}, false
	}
	key := ns + "/" + name
	if visited[key] {
		return traefikMissingRef{}, false
	}
	visited[key] = true
	mw, ok := middlewares.objects[key]
	if !ok {
		// Legacy Traefik names flatten namespace and name with a dash, so
		// another object's configuration can satisfy this reference.
		if middlewares.generatedNames[identifier] > 0 {
			return traefikMissingRef{}, false
		}
		return traefikMissingRef{
			reason: TraefikRouterMissingMiddlewareReason,
			target: fmt.Sprintf("Middleware %q", traefikRefLabel(ns, name)),
			reads:  []issuesapi.EvidenceRead{{Group: inv.group, Resource: "middlewares", Namespace: ns, Verb: "list"}},
		}, true
	}
	if chain, _, _ := unstructured.NestedSlice(mw.Object, "spec", "chain", "middlewares"); len(chain) > 0 {
		for _, raw := range chain {
			member, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if missing, ok := inv.middlewareMissing(member, ns, allowCrossNamespace, visited); ok {
				if missing.via == "" {
					missing.via = fmt.Sprintf("chain Middleware %q", traefikRefLabel(ns, name))
				}
				missing.reads = append(missing.reads, issuesapi.EvidenceRead{Group: inv.group, Resource: "middlewares", Namespace: ns, Verb: "list"})
				return missing, true
			}
		}
	}
	if errSvc, found, _ := unstructured.NestedMap(mw.Object, "spec", "errors", "service"); found {
		svcNs := ns
		if refNs, _ := errSvc["namespace"].(string); refNs != "" {
			if refNs != ns && !allowCrossNamespace {
				return traefikMissingRef{}, false
			}
			svcNs = refNs
		}
		if target, read, ok := inv.serviceMissing(errSvc, svcNs); ok {
			return traefikMissingRef{
				reason: TraefikRouterMissingServiceReason,
				target: target,
				via:    fmt.Sprintf("errors Middleware %q", traefikRefLabel(ns, name)),
				reads:  []issuesapi.EvidenceRead{read, {Group: inv.group, Resource: "middlewares", Namespace: ns, Verb: "list"}},
			}, true
		}
	}
	return traefikMissingRef{}, false
}

func (inv *traefikInventory) ruleMissingRefs(r map[string]any, ns string, allowCrossNamespace bool) []traefikMissingRef {
	var missing []traefikMissingRef
	for _, svc := range nestedMaps(r, "services") {
		svcNs := ns
		if refNs, _ := svc["namespace"].(string); refNs != "" {
			if refNs != ns && !allowCrossNamespace {
				continue
			}
			svcNs = refNs
		}
		if target, read, ok := inv.serviceMissing(svc, svcNs); ok {
			missing = append(missing, traefikMissingRef{reason: TraefikRouterMissingServiceReason, target: target, reads: []issuesapi.EvidenceRead{read}})
		}
	}
	for _, mw := range nestedMaps(r, "middlewares") {
		if m, ok := inv.middlewareMissing(mw, ns, allowCrossNamespace, map[string]bool{}); ok {
			missing = append(missing, m)
		}
	}
	return missing
}

func (inv *traefikInventory) shadowingRoute(route *unstructured.Unstructured, ruleIndex int, match string, loaders []traefikInstance) (issuesapi.EvidenceRead, bool) {
	routes := inv.load("IngressRoute")
	if routes == nil || match == "" {
		return issuesapi.EvidenceRead{}, false
	}
	for _, candidate := range routes.objects {
		if candidate.GetDeletionTimestamp() != nil {
			continue
		}
		for _, inst := range traefikLoaders(loaders, candidate) {
			overlaps := false
			entryPoints := inst.routeEntryPoints(route)
			for ep := range inst.routeEntryPoints(candidate) {
				overlaps = overlaps || entryPoints[ep]
			}
			if !overlaps {
				continue
			}
			rules, _, _ := unstructured.NestedSlice(candidate.Object, "spec", "routes")
			for i, raw := range rules {
				if candidate.GetNamespace() == route.GetNamespace() && candidate.GetName() == route.GetName() && i == ruleIndex {
					continue
				}
				r, ok := raw.(map[string]any)
				if !ok || r["match"] != match || (r["kind"] != nil && r["kind"] != "" && r["kind"] != "Rule") || len(nestedMaps(r, "services")) == 0 {
					continue
				}
				allowed := true
				for _, ref := range append(nestedMaps(r, "services"), nestedMaps(r, "middlewares")...) {
					if ns, _ := ref["namespace"].(string); ns != "" && ns != candidate.GetNamespace() && !inst.allowCrossNamespace {
						allowed = false
					}
				}
				if allowed && len(inv.ruleMissingRefs(r, candidate.GetNamespace(), inst.allowCrossNamespace)) == 0 {
					return issuesapi.EvidenceRead{Group: inv.group, Resource: "ingressroutes", Namespace: candidate.GetNamespace(), Verb: "list"}, true
				}
			}
		}
	}
	return issuesapi.EvidenceRead{}, false
}

func traefikRouteMissingRefs(route *unstructured.Unstructured, group string, loaders []traefikInstance, inv *traefikInventory, now time.Time) []Detection {
	ns := route.GetNamespace()
	allowCrossNamespace := false
	podNamespaces := map[string]bool{}
	for _, inst := range loaders {
		allowCrossNamespace = allowCrossNamespace || inst.allowCrossNamespace
		podNamespaces[inst.podNamespace] = true
	}
	routes, _, _ := unstructured.NestedSlice(route.Object, "spec", "routes")
	seen := map[string]int{}
	var out []Detection
	for i, raw := range routes {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		match, _ := r["match"].(string)
		missing := inv.ruleMissingRefs(r, ns, allowCrossNamespace)
		if len(missing) == 0 {
			continue
		}
		shadowRead, shadowed := inv.shadowingRoute(route, i, match, loaders)
		for _, m := range missing {
			key := m.reason + "\x00" + m.target + "\x00" + m.via
			message := fmt.Sprintf("routes[%d] (%s) references %s, which does not exist", i, match, m.target)
			action := fmt.Sprintf("Create %s, or point routes[%d] at one that exists.", m.target, i)
			if m.via != "" {
				message = fmt.Sprintf("routes[%d] (%s) uses %s, which references %s, which does not exist", i, match, m.via, m.target)
				action = fmt.Sprintf("Create %s, fix %s, or remove it from routes[%d].", m.target, m.via, i)
			}
			d := missingRefProblem(now, "IngressRoute", group, ns, route.GetName(), m.reason, message, route.GetCreationTimestamp().Time)
			d.Cause = fmt.Sprintf("Traefik does not load a router when a Service or Middleware it needs is missing, so requests matching %s get no route from it: a 404 unless another router matches. Traefik reports this only in its log and dashboard.", match)
			if shadowed {
				d.Severity = "warning"
				d.Cause += " Another route with the same rule may serve these requests."
			}
			d.Action = action
			reads := dedupeEvidenceReads(m.reads)
			if shadowed {
				reads = append(reads, shadowRead)
			}
			for _, podNs := range sortedSetKeys(podNamespaces) {
				reads = append(reads, issuesapi.EvidenceRead{Resource: "pods", Namespace: podNs, Verb: "list"})
			}
			d.RequiredReads = reads
			if index, exists := seen[key]; exists {
				if out[index].Severity == "warning" && d.Severity == "critical" {
					out[index] = d
				}
				continue
			}
			seen[key] = len(out)
			out = append(out, d)
		}
	}
	return out
}

func traefikRefLabel(ns, name string) string {
	return ns + "/" + name
}

func traefikGeneratedName(ns, name string) string {
	return strings.Join(strings.FieldsFunc(ns+"-"+name, func(c rune) bool {
		return !unicode.IsLetter(c) && !unicode.IsNumber(c)
	}), "-")
}

func nestedMaps(m map[string]any, key string) []map[string]any {
	raw, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if mm, ok := item.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

func dedupeEvidenceReads(in []issuesapi.EvidenceRead) []issuesapi.EvidenceRead {
	seen := map[issuesapi.EvidenceRead]bool{}
	out := make([]issuesapi.EvidenceRead, 0, len(in))
	for _, r := range in {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// addTraefikIngressRouteTLSReferences adds the certificates IngressRoutes
// name to Traefik's pool: Traefik loads them into the same store as its
// Ingress and Gateway certificates, so a valid one can be what clients get
// for a host an Ingress also serves. When IngressRoutes exist but Radar can't
// list them all, the pool's competitors are unknown.
func addTraefikIngressRouteTLSReferences(idx *tlsServingIndex) {
	dynamicCache := GetDynamicResourceCache()
	discovery := GetResourceDiscovery()
	if dynamicCache == nil || discovery == nil {
		return
	}
	gvr, ok := discovery.GetGVRWithGroup("IngressRoute", traefikGroup)
	if !ok {
		return
	}
	if !dynamicCache.IsClusterWideSynced(gvr) {
		idx.uncertain[traefikIngressController] = true
		return
	}
	routes, err := dynamicCache.ListWatched(gvr)
	if err != nil {
		idx.uncertain[traefikIngressController] = true
		return
	}
	for _, route := range routes {
		if secretName, _, _ := unstructured.NestedString(route.Object, "spec", "tls", "secretName"); secretName != "" {
			idx.reference(traefikIngressController, route.GetNamespace(), secretName, "")
		}
	}
}
