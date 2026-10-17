package k8s

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/skyhook-io/radar/internal/imageutil"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

const (
	ServiceMixedBackendsReason      = "Routes to different applications"
	ServiceMixedBackendsFingerprint = "svc:mixed-backends"
)

// MeshProxyContainers maps the container name a service mesh injects to the
// mesh's name. Running proxies are the strongest evidence that a pod is meshed,
// and they are infrastructure, never the application a Service fronts.
var MeshProxyContainers = map[string]string{
	"istio-proxy":      "Istio",
	"linkerd-proxy":    "Linkerd",
	"consul-dataplane": "Consul",
}

// ServiceBackendWorkload is one workload whose Pods a Service selects. A Pod
// with no controller is its own workload (Kind "Pod").
type ServiceBackendWorkload struct {
	Group string
	Kind  string
	Name  string
	// Label is Name, or Kind/Name when two selected workloads share a name.
	Label string
	Pods  int
	Ready int
}

// ServicePortGap is a numeric targetPort that some backend workloads declare
// and others, which declare ports of their own, do not.
type ServicePortGap struct {
	Port     int32
	Protocol corev1.Protocol
	Serving  []string
	Missing  []string
}

// ServiceBackends groups a Service's selected Pods by workload and records the
// evidence that the workloads are different applications.
//
// Several workloads behind one Service is often deliberate (stable + canary
// Deployments, blue/green, an Argo Rollout canary without a traffic router), so
// the grouping alone is context. Applications and PortGaps are the evidence of
// a mistake, and they are computed only over workloads that receive traffic:
// Pods that are endpoints (ready and not terminating, or any live Pod when the
// Service publishes not-ready addresses) and that resolve at least one of the
// Service's ports.
type ServiceBackends struct {
	Workloads []ServiceBackendWorkload
	// Applications lists, when the receiving workloads run no image
	// repository in common, the workload names of each application.
	Applications [][]string
	PortGaps     []ServicePortGap
	// ResolvedThroughReplicaSets is set when a workload name came from a
	// cached ReplicaSet's owner rather than from the Pod itself.
	ResolvedThroughReplicaSets bool
}

// MultipleWorkloads reports whether the selected Pods come from more than one
// workload.
func (b ServiceBackends) MultipleWorkloads() bool { return len(b.Workloads) > 1 }

// DifferentApplications reports whether the evidence says the receiving
// workloads are different applications.
func (b ServiceBackends) DifferentApplications() bool {
	return len(b.Applications) > 1 || len(b.PortGaps) > 0
}

const maxBackendWorkloadsListed = 5

// Summary reads "3 pods from 2 workloads: frontend (2), search (1)".
func (b ServiceBackends) Summary() string {
	total := 0
	parts := make([]string, 0, len(b.Workloads))
	for _, w := range b.Workloads {
		total += w.Pods
		parts = append(parts, fmt.Sprintf("%s (%d)", w.Label, w.Pods))
	}
	return fmt.Sprintf("%s from %d workloads: %s", plural(total, "pod"), len(b.Workloads), strings.Join(capNames(parts), ", "))
}

// capNames keeps a list readable when a selector reaches many workloads.
func capNames(names []string) []string {
	if len(names) <= maxBackendWorkloadsListed {
		return names
	}
	return append(names[:maxBackendWorkloadsListed:maxBackendWorkloadsListed], fmt.Sprintf("%d more", len(names)-maxBackendWorkloadsListed))
}

// Problem is the issue message for a Service whose backends are different
// applications, e.g. "Service frontend routes to 2 different applications
// (frontend, search); search's pods don't declare port 5000".
func (b ServiceBackends) Problem(service string) string {
	names := b.applicationNames()
	return fmt.Sprintf("Service %s routes to %d different applications (%s)", service, len(names), strings.Join(capNames(names), ", ")) + b.portGapClauses("; ")
}

// Cause explains the Problem in plain terms, e.g. "The selector matches pods
// of frontend and search, so requests are spread across all of them; search's
// pods don't declare port 5000, so requests that reach them may fail."
func (b ServiceBackends) Cause() string {
	cause := fmt.Sprintf("The selector matches pods of %s, so requests are spread across all of them", joinNames(b.applicationNames()))
	if len(b.PortGaps) > 0 {
		cause += b.portGapClauses("; ") + ", so requests that reach them may fail"
	}
	return cause + "."
}

// applicationNames labels each application; one made of several workloads
// reads "a + b".
func (b ServiceBackends) applicationNames() []string {
	apps := b.Applications
	if len(apps) < 2 {
		apps = nil
		for _, name := range b.gapWorkloadNames() {
			apps = append(apps, []string{name})
		}
	}
	names := make([]string, len(apps))
	for i, app := range apps {
		names[i] = strings.Join(capNames(app), " + ")
	}
	return names
}

func (b ServiceBackends) portGapClauses(sep string) string {
	var out string
	for _, g := range b.PortGaps {
		port := strconv.Itoa(int(g.Port))
		if g.Protocol != corev1.ProtocolTCP {
			port += "/" + string(g.Protocol)
		}
		out += fmt.Sprintf("%s%s's pods don't declare port %s", sep, joinNames(g.Missing), port)
	}
	return out
}

// gapWorkloadNames lists every workload on either side of a port gap: the
// ones that declare the port first.
func (b ServiceBackends) gapWorkloadNames() []string {
	var serving, missing []string
	for _, w := range b.Workloads {
		for _, g := range b.PortGaps {
			if containsString(g.Serving, w.Label) {
				serving = append(serving, w.Label)
				break
			}
		}
	}
	for _, w := range b.Workloads {
		if containsString(serving, w.Label) {
			continue
		}
		for _, g := range b.PortGaps {
			if containsString(g.Missing, w.Label) {
				missing = append(missing, w.Label)
				break
			}
		}
	}
	return append(serving, missing...)
}

func joinNames(names []string) string {
	names = capNames(names)
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// PodWorkload resolves the workload a human would name for a Pod. A ReplicaSet
// resolves to its own controller (a Deployment or an Argo Rollout) when the
// ReplicaSet is cached, and stays itself when it isn't: guessing a Deployment
// from the name would invent an object. Every other controller is the
// workload. ok is false for a Pod with no controller.
func PodWorkload(cache *ResourceCache, pod *corev1.Pod) (TopOwnerInfo, bool) {
	ref, ok, _ := podWorkload(cache, pod)
	return ref, ok
}

// podWorkload also reports whether the owner is known to be the workload: false
// for a ReplicaSet that couldn't be read, whose real parent is unknown.
func podWorkload(cache *ResourceCache, pod *corev1.Pod) (ref TopOwnerInfo, ok, settled bool) {
	if pod == nil {
		return TopOwnerInfo{}, false, true
	}
	owner := controllerOwnerRef(pod.OwnerReferences)
	if owner == nil {
		return TopOwnerInfo{}, false, true
	}
	ref = TopOwnerInfo{Group: schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind).Group, Kind: owner.Kind, Name: owner.Name}
	if owner.Kind != "ReplicaSet" {
		return ref, true, true
	}
	if cache == nil || cache.ReplicaSets() == nil {
		return ref, true, false
	}
	rs, err := cache.ReplicaSets().ReplicaSets(pod.Namespace).Get(owner.Name)
	if err != nil || rs == nil {
		return ref, true, false
	}
	if parent := controllerOwnerRef(rs.OwnerReferences); parent != nil {
		return TopOwnerInfo{Group: schema.FromAPIVersionAndKind(parent.APIVersion, parent.Kind).Group, Kind: parent.Kind, Name: parent.Name}, true, true
	}
	return ref, true, true
}

type backendContainer struct {
	name  string
	repo  string
	ports []corev1.ContainerPort
	// nativeSidecar marks a restartable init container: it can serve a port,
	// but it is infrastructure next to the application.
	nativeSidecar bool
}

// signature identifies a container across workloads: the same name, image
// repository and declared ports. A container with the same signature in every
// receiving workload (a namespace-wide injected sidecar, a shared auth proxy,
// the application container of a same-image canary) says nothing about how
// the workloads differ.
func (c backendContainer) signature() string {
	ports := make([]string, len(c.ports))
	for i, p := range c.ports {
		ports[i] = fmt.Sprintf("%s/%d/%s", p.Name, p.ContainerPort, portProtocol(p.Protocol))
	}
	sort.Strings(ports)
	return c.name + "\x00" + c.repo + "\x00" + strings.Join(ports, ",")
}

type backendWorkload struct {
	ServiceBackendWorkload
	receiving  []*corev1.Pod
	containers map[string]backendContainer
	// repos are the image repositories of the receiving Pods' containers,
	// mesh proxies aside.
	repos map[string]bool
}

// AnalyzeServiceBackends groups the Pods a Service selects by workload and
// checks whether the workloads that receive its traffic are different
// applications. pods are the Pods matching the Service's selector; finished
// Pods are ignored. Headless Services are grouped but never judged: they
// usually publish peers for discovery, where mixing workloads is the point.
func AnalyzeServiceBackends(cache *ResourceCache, svc *corev1.Service, pods []*corev1.Pod) ServiceBackends {
	var out ServiceBackends
	if svc == nil {
		return out
	}
	byKey := map[string]*backendWorkload{}
	var order []*backendWorkload
	// Revisions of one workload whose ReplicaSets couldn't be read show up as
	// separate ReplicaSets; judging them as different applications would be
	// a guess, so their name prefixes are counted to hold the judgment back.
	unsettledRevisions := map[string]map[string]bool{}
	for _, pod := range pods {
		if pod == nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		ref, ok, settled := podWorkload(cache, pod)
		if !ok {
			ref = TopOwnerInfo{Kind: "Pod", Name: pod.Name}
		}
		if !settled {
			prefix := stripReplicaSetHash(ref.Name)
			if unsettledRevisions[prefix] == nil {
				unsettledRevisions[prefix] = map[string]bool{}
			}
			unsettledRevisions[prefix][ref.Name] = true
		}
		if owner := controllerOwnerRef(pod.OwnerReferences); owner != nil && owner.Kind == "ReplicaSet" && ref.Kind != "ReplicaSet" {
			out.ResolvedThroughReplicaSets = true
		}
		key := ref.Group + "/" + ref.Kind + "/" + ref.Name
		w := byKey[key]
		if w == nil {
			w = &backendWorkload{ServiceBackendWorkload: ServiceBackendWorkload{Group: ref.Group, Kind: ref.Kind, Name: ref.Name}}
			byKey[key] = w
			order = append(order, w)
		}
		w.Pods++
		ready := isPodReadyForProblem(pod)
		if ready {
			w.Ready++
		}
		// EndpointSlice readiness: publishNotReadyAddresses publishes every
		// live Pod, terminating or not. A Pod that resolves none of the
		// Service's ports is in no endpoint.
		if (svc.Spec.PublishNotReadyAddresses || (ready && pod.DeletionTimestamp == nil)) && resolvesAnyServicePort(svc, []*corev1.Pod{pod}) {
			w.receiving = append(w.receiving, pod)
		}
	}
	// The workload named like the Service is most likely the one it was
	// written for, so it leads; then the largest.
	sort.SliceStable(order, func(i, j int) bool {
		if mi, mj := order[i].Name == svc.Name, order[j].Name == svc.Name; mi != mj {
			return mi
		}
		if order[i].Pods != order[j].Pods {
			return order[i].Pods > order[j].Pods
		}
		if order[i].Kind != order[j].Kind {
			return order[i].Kind < order[j].Kind
		}
		return order[i].Name < order[j].Name
	})
	names := map[string]int{}
	for _, w := range order {
		names[w.Name]++
	}
	for _, w := range order {
		w.Label = w.Name
		if names[w.Name] > 1 {
			w.Label = w.Kind + "/" + w.Name
		}
		out.Workloads = append(out.Workloads, w.ServiceBackendWorkload)
	}
	if len(order) < 2 || svc.Spec.ClusterIP == corev1.ClusterIPNone {
		return out
	}
	for _, revisions := range unsettledRevisions {
		if len(revisions) > 1 {
			return out
		}
	}

	var receiving []*backendWorkload
	for _, w := range order {
		if len(w.receiving) > 0 {
			w.containers = workloadContainers(w.receiving)
			w.repos = containerRepos(w.receiving)
			receiving = append(receiving, w)
		}
	}
	if len(receiving) < 2 {
		return out
	}
	common := commonContainerSignatures(receiving)
	out.PortGaps = servicePortGaps(svc, receiving, common)
	out.Applications = distinctApplicationsPerPort(svc, receiving)
	return out
}

// distinctApplicationsPerPort compares the workloads behind each Service port
// separately: Kubernetes builds endpoints per port, so two applications that
// each serve a different named port never share traffic.
func distinctApplicationsPerPort(svc *corev1.Service, workloads []*backendWorkload) [][]string {
	if len(svc.Spec.Ports) == 0 {
		return distinctApplications(workloads)
	}
	var worst [][]string
	for _, sp := range svc.Spec.Ports {
		behind := workloads
		if sp.TargetPort.Type == intstr.String && sp.TargetPort.StrVal != "" {
			behind = nil
			for _, w := range workloads {
				for _, pod := range w.receiving {
					if declaresNamedPort(pod, sp.TargetPort.StrVal, portProtocol(sp.Protocol)) {
						behind = append(behind, w)
						break
					}
				}
			}
		}
		if apps := distinctApplications(behind); len(apps) > len(worst) {
			worst = apps
		}
	}
	return worst
}

func workloadContainers(pods []*corev1.Pod) map[string]backendContainer {
	out := map[string]backendContainer{}
	for _, pod := range pods {
		for _, c := range pod.Spec.Containers {
			bc := backendContainer{name: c.Name, repo: comparableImageRepository(c.Image), ports: c.Ports}
			out[bc.signature()] = bc
		}
		for _, c := range pod.Spec.InitContainers {
			if isNativeSidecar(&c) {
				bc := backendContainer{name: c.Name, repo: comparableImageRepository(c.Image), ports: c.Ports, nativeSidecar: true}
				out[bc.signature()] = bc
			}
		}
	}
	return out
}

// containerRepos collects the image repositories the Pods run, mesh proxies
// aside. Container order and sidecars don't matter: two workloads that run any
// repository in common count as one application.
func containerRepos(pods []*corev1.Pod) map[string]bool {
	out := map[string]bool{}
	for _, pod := range pods {
		for _, c := range pod.Spec.Containers {
			if MeshProxyContainers[c.Name] != "" {
				continue
			}
			if repo := comparableImageRepository(c.Image); repo != "" {
				out[repo] = true
			}
		}
	}
	return out
}

func commonContainerSignatures(workloads []*backendWorkload) map[string]bool {
	common := map[string]bool{}
	for sig := range workloads[0].containers {
		shared := true
		for _, w := range workloads[1:] {
			if _, ok := w.containers[sig]; !ok {
				shared = false
				break
			}
		}
		if shared {
			common[sig] = true
		}
	}
	return common
}

// appContainers are the containers that can tell workloads apart: not shared
// identically by every receiving workload, not a mesh proxy and not a native
// sidecar.
func appContainers(w *backendWorkload, common map[string]bool) []backendContainer {
	var out []backendContainer
	for sig, c := range w.containers {
		if common[sig] || c.nativeSidecar || MeshProxyContainers[c.name] != "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// resolvesAnyServicePort reports whether the Pods are endpoints for at least
// one Service port. A named targetPort resolves, as in Kubernetes, only on a
// Pod whose regular or native sidecar container declares a port with that name
// and the Service port's protocol; a numeric one resolves on every Pod.
func resolvesAnyServicePort(svc *corev1.Service, pods []*corev1.Pod) bool {
	if len(svc.Spec.Ports) == 0 {
		return true
	}
	for _, sp := range svc.Spec.Ports {
		if sp.TargetPort.Type != intstr.String || sp.TargetPort.StrVal == "" {
			return true
		}
		for _, pod := range pods {
			if declaresNamedPort(pod, sp.TargetPort.StrVal, portProtocol(sp.Protocol)) {
				return true
			}
		}
	}
	return false
}

func declaresNamedPort(pod *corev1.Pod, name string, proto corev1.Protocol) bool {
	matches := func(c *corev1.Container) bool {
		for _, cp := range c.Ports {
			if cp.Name == name && portProtocol(cp.Protocol) == proto {
				return true
			}
		}
		return false
	}
	for i := range pod.Spec.Containers {
		if matches(&pod.Spec.Containers[i]) {
			return true
		}
	}
	for i := range pod.Spec.InitContainers {
		if isNativeSidecar(&pod.Spec.InitContainers[i]) && matches(&pod.Spec.InitContainers[i]) {
			return true
		}
	}
	return false
}

// servicePortGaps finds numeric targetPorts that some receiving workloads
// declare while others declare application ports but not this one. A
// workload that declares no ports at all is not evidence: containerPort is
// optional and many images never declare it.
func servicePortGaps(svc *corev1.Service, workloads []*backendWorkload, common map[string]bool) []ServicePortGap {
	var out []ServicePortGap
	for _, sp := range svc.Spec.Ports {
		if sp.TargetPort.Type == intstr.String {
			continue
		}
		port := sp.TargetPort.IntVal
		if port == 0 {
			port = sp.Port
		}
		proto := portProtocol(sp.Protocol)
		gap := ServicePortGap{Port: port, Protocol: proto}
		for _, w := range workloads {
			if declaresPort(w.containers, port, proto) {
				gap.Serving = append(gap.Serving, w.Label)
				continue
			}
			for _, c := range appContainers(w, common) {
				if len(c.ports) > 0 {
					gap.Missing = append(gap.Missing, w.Label)
					break
				}
			}
		}
		if len(gap.Serving) > 0 && len(gap.Missing) > 0 {
			out = append(out, gap)
		}
	}
	return out
}

func declaresPort(containers map[string]backendContainer, port int32, proto corev1.Protocol) bool {
	for _, c := range containers {
		for _, cp := range c.ports {
			if cp.ContainerPort == port && portProtocol(cp.Protocol) == proto {
				return true
			}
		}
	}
	return false
}

// distinctApplications joins workloads that run an image repository in
// common; more than one group means different applications. Sharing anything
// counts, so a sidecar injected into every Pod hides this signal: a missed
// warning costs less than a false one, and the port signal still applies.
func distinctApplications(workloads []*backendWorkload) [][]string {
	parent := make([]int, len(workloads))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	repoOwner := map[string]int{}
	hasRepo := make([]bool, len(workloads))
	for i, w := range workloads {
		for repo := range w.repos {
			hasRepo[i] = true
			if j, ok := repoOwner[repo]; ok {
				parent[find(i)] = find(j)
			} else {
				repoOwner[repo] = i
			}
		}
	}
	groups := map[int][]string{}
	var roots []int
	for i, w := range workloads {
		if !hasRepo[i] {
			continue
		}
		r := find(i)
		if _, ok := groups[r]; !ok {
			roots = append(roots, r)
		}
		groups[r] = append(groups[r], w.Label)
	}
	if len(roots) < 2 {
		return nil
	}
	out := make([][]string, 0, len(roots))
	for _, r := range roots {
		out = append(out, groups[r])
	}
	return out
}

// comparableImageRepository drops the tag and digest and Docker Hub's implicit
// registry and library prefixes, so "nginx:1.27" and "docker.io/library/nginx"
// compare equal.
func comparableImageRepository(image string) string {
	repo := strings.ToLower(imageutil.Repository(strings.TrimSpace(image)))
	for _, prefix := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		repo = strings.TrimPrefix(repo, prefix)
	}
	return strings.TrimPrefix(repo, "library/")
}

func portProtocol(p corev1.Protocol) corev1.Protocol {
	if p == "" {
		return corev1.ProtocolTCP
	}
	return p
}

// ServiceBackendLookup serves resource context's Service backend lookups from
// the cache.
type ServiceBackendLookup struct {
	Cache *ResourceCache
}

func (l ServiceBackendLookup) PodsForServiceSelector(namespace string, selector labels.Selector) ([]*corev1.Pod, error) {
	if l.Cache == nil || l.Cache.Pods() == nil {
		return nil, nil
	}
	return l.Cache.Pods().Pods(namespace).List(selector)
}

func (l ServiceBackendLookup) GroupServiceBackends(svc *corev1.Service, pods []*corev1.Pod) resourcecontext.ServiceBackendGrouping {
	backends := AnalyzeServiceBackends(l.Cache, svc, pods)
	out := resourcecontext.ServiceBackendGrouping{}
	for _, w := range backends.Workloads {
		namespace := svc.Namespace
		if w.Kind != "Pod" {
			// The access check passes kinds it can't resolve, so a workload
			// of an unknown kind would be named without one.
			if _, resource := LookupResourceGVR(w.Kind, w.Group); resource == "" {
				return resourcecontext.ServiceBackendGrouping{}
			}
			if clusterScoped, _, _ := ClassifyKindScope(w.Kind, w.Group); clusterScoped {
				namespace = ""
			}
		}
		out.Workloads = append(out.Workloads, resourcecontext.ServiceBackendWorkload{
			ContextRef: resourcecontext.ContextRef{Kind: w.Kind, Group: w.Group, Namespace: namespace, Name: w.Name},
			Pods:       w.Pods,
			Ready:      w.Ready,
		})
	}
	if backends.DifferentApplications() {
		out.DifferentApplications = backends.Problem(svc.Name)
	}
	return out
}
