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
	ServiceMixedBackendsReason      = "Routes to mismatched backends"
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
	// Applications lists, when the receiving workloads run no image name in
	// common, the workload names of each application.
	Applications [][]string
	PortGaps     []ServicePortGap
	// ResolvedThroughReplicaSets is set when a workload name came from a
	// cached ReplicaSet's owner rather than from the Pod itself.
	ResolvedThroughReplicaSets bool
}

// MultipleWorkloads reports whether the selected Pods come from more than one
// workload.
func (b ServiceBackends) MultipleWorkloads() bool { return len(b.Workloads) > 1 }

// Mismatched reports whether the evidence says the receiving workloads don't
// all serve this Service: different applications, or a port some don't
// declare.
func (b ServiceBackends) Mismatched() bool {
	return b.differentImages() || len(b.PortGaps) > 0
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

// Problem is the issue message for a Service with mismatched backends. It says
// "different applications" only when the image names show it, e.g.
// "Service frontend routes to 2 different applications (frontend, search)";
// a port gap alone reads "Service frontend routes to 2 workloads; search's
// pods don't declare port 5000".
func (b ServiceBackends) Problem(service string) string {
	names := b.applicationNames()
	if b.differentImages() {
		return fmt.Sprintf("Service %s routes to %d different applications (%s)", service, len(names), strings.Join(capNames(names), ", ")) + b.portGapClauses("; ")
	}
	return fmt.Sprintf("Service %s routes to %d workloads", service, len(names)) + b.portGapClauses("; ")
}

// Noun is what the backends are, for wording that can't name them:
// "application" when the image names differ, else "workload".
func (b ServiceBackends) Noun() string {
	if b.differentImages() {
		return "application"
	}
	return "workload"
}

func (b ServiceBackends) differentImages() bool { return len(b.Applications) > 1 }

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
// ones that declare the port first. Gaps carry the names judged, which for
// unread revisions is their merged name rather than a single ReplicaSet's.
func (b ServiceBackends) gapWorkloadNames() []string {
	seen := map[string]bool{}
	var serving, missing []string
	for _, g := range b.PortGaps {
		for _, n := range g.Serving {
			if !seen[n] {
				seen[n] = true
				serving = append(serving, n)
			}
		}
	}
	for _, g := range b.PortGaps {
		for _, n := range g.Missing {
			if !seen[n] {
				seen[n] = true
				missing = append(missing, n)
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
// name and declared ports. A container with the same signature in every
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
	// separate ReplicaSets. Those that share a name prefix are judged as one
	// workload, never against each other.
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
		// live Pod with an IP, terminating or not. A Pod without an IP, or
		// that resolves none of the Service's ports, is in no endpoint.
		if pod.Status.PodIP != "" && (svc.Spec.PublishNotReadyAddresses || (ready && pod.DeletionTimestamp == nil)) && resolvesAnyServicePort(svc, []*corev1.Pod{pod}) {
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
	revisionOf := map[string]string{}
	for prefix, revisions := range unsettledRevisions {
		if len(revisions) > 1 {
			for name := range revisions {
				revisionOf[name] = prefix
			}
		}
	}

	var receiving []*backendWorkload
	merged := map[string]*backendWorkload{}
	for _, w := range order {
		if len(w.receiving) == 0 {
			continue
		}
		prefix, isRevision := revisionOf[w.Name]
		if !isRevision || w.Kind != "ReplicaSet" {
			receiving = append(receiving, w)
			continue
		}
		if m := merged[prefix]; m != nil {
			m.receiving = append(m.receiving, w.receiving...)
			continue
		}
		m := &backendWorkload{ServiceBackendWorkload: ServiceBackendWorkload{Group: w.Group, Kind: w.Kind, Name: prefix, Label: prefix}, receiving: append([]*corev1.Pod(nil), w.receiving...)}
		merged[prefix] = m
		receiving = append(receiving, m)
	}
	for _, w := range receiving {
		w.containers = workloadContainers(w.receiving)
	}
	if len(receiving) < 2 {
		return out
	}
	common := commonContainerSignatures(receiving)
	out.PortGaps = servicePortGaps(svc, receiving, common)
	out.Applications = distinctApplicationsPerPort(svc, receiving)
	return out
}

// distinctApplicationsPerPort compares the Pods behind each Service port
// separately: Kubernetes builds endpoints per port, so two applications that
// each serve a different named port never share traffic.
func distinctApplicationsPerPort(svc *corev1.Service, workloads []*backendWorkload) [][]string {
	behindPort := func(sp *corev1.ServicePort) ([]string, []map[string]bool) {
		var labels []string
		var repos []map[string]bool
		var podGroups [][]*corev1.Pod
		for _, w := range workloads {
			pods := w.receiving
			if sp != nil && sp.TargetPort.Type == intstr.String && sp.TargetPort.StrVal != "" {
				pods = nil
				for _, pod := range w.receiving {
					if declaresNamedPort(pod, sp.TargetPort.StrVal, portProtocol(sp.Protocol)) {
						pods = append(pods, pod)
					}
				}
			}
			if len(pods) > 0 {
				labels = append(labels, w.Label)
				repos = append(repos, containerRepos(pods))
				podGroups = append(podGroups, pods)
			}
		}
		if versionsOfOneApplication(podGroups) || (sp != nil && sameServerForPort(sp, podGroups)) {
			return nil, nil
		}
		return labels, repos
	}
	if len(svc.Spec.Ports) == 0 {
		return distinctApplications(behindPort(nil))
	}
	var worst [][]string
	for i := range svc.Spec.Ports {
		if apps := distinctApplications(behindPort(&svc.Spec.Ports[i])); len(apps) > len(worst) {
			worst = apps
		}
	}
	return worst
}

// sameServerForPort reports whether one identical container (same name and
// image name), such as a mesh proxy, serves the port in every workload:
// then traffic to that port reaches the same software whatever else the Pods
// run.
func sameServerForPort(sp *corev1.ServicePort, podGroups [][]*corev1.Pod) bool {
	if len(podGroups) < 2 {
		return false
	}
	var shared map[string]bool
	for _, pods := range podGroups {
		servers := map[string]bool{}
		for _, pod := range pods {
			for _, c := range podPortContainers(pod) {
				if containerServesPort(c, sp) {
					servers[c.Name+"\x00"+comparableImageName(c.Image)] = true
				}
			}
		}
		if shared == nil {
			shared = servers
			continue
		}
		for k := range shared {
			if !servers[k] {
				delete(shared, k)
			}
		}
	}
	return len(shared) > 0
}

// podPortContainers are the containers that can serve a Service port: regular
// containers and native sidecars.
func podPortContainers(pod *corev1.Pod) []*corev1.Container {
	var out []*corev1.Container
	for i := range pod.Spec.Containers {
		out = append(out, &pod.Spec.Containers[i])
	}
	for i := range pod.Spec.InitContainers {
		if isNativeSidecar(&pod.Spec.InitContainers[i]) {
			out = append(out, &pod.Spec.InitContainers[i])
		}
	}
	return out
}

func containerServesPort(c *corev1.Container, sp *corev1.ServicePort) bool {
	proto := portProtocol(sp.Protocol)
	for _, cp := range c.Ports {
		if portProtocol(cp.Protocol) != proto {
			continue
		}
		switch {
		case sp.TargetPort.Type == intstr.String && sp.TargetPort.StrVal != "":
			if cp.Name == sp.TargetPort.StrVal {
				return true
			}
		default:
			target := sp.TargetPort.IntVal
			if target == 0 {
				target = sp.Port
			}
			if cp.ContainerPort == target {
				return true
			}
		}
	}
	return false
}

// Pods labelled with one application name and a version are versions of one
// application, however their images are named:
// Istio's convention (app, version) and the recommended labels
// (app.kubernetes.io/name, app.kubernetes.io/version) both say so, and the
// Istio sample gives each version its own image repository.
var (
	appNameLabels    = []string{"app.kubernetes.io/name", "app"}
	appVersionLabels = []string{"app.kubernetes.io/version", "version"}
)

func versionsOfOneApplication(podGroups [][]*corev1.Pod) bool {
	if len(podGroups) < 2 {
		return false
	}
	// One app-name label must be on every Pod with one value; any app-name
	// label that carries two different values tells the workloads apart. A
	// label only some Pods carry is neither.
	sharedName := false
	for _, key := range appNameLabels {
		values := map[string]bool{}
		for _, pods := range podGroups {
			for _, pod := range pods {
				if v := pod.Labels[key]; v != "" {
					values[v] = true
				}
			}
		}
		if len(values) > 1 {
			return false
		}
		if len(values) == 1 && everyPodHasLabel(podGroups, key) {
			sharedName = true
		}
	}
	if !sharedName {
		return false
	}
	// Versions may repeat (a stable and a canary of v2) or mix within one
	// workload mid-rollout; what marks a version is that every Pod has one.
	for _, key := range appVersionLabels {
		if everyPodHasLabel(podGroups, key) {
			return true
		}
	}
	return false
}

func everyPodHasLabel(podGroups [][]*corev1.Pod, key string) bool {
	for _, pods := range podGroups {
		for _, pod := range pods {
			if pod.Labels[key] == "" {
				return false
			}
		}
	}
	return true
}

func workloadContainers(pods []*corev1.Pod) map[string]backendContainer {
	out := map[string]backendContainer{}
	for _, pod := range pods {
		for _, c := range pod.Spec.Containers {
			bc := backendContainer{name: c.Name, repo: comparableImageName(c.Image), ports: c.Ports}
			out[bc.signature()] = bc
		}
		for _, c := range pod.Spec.InitContainers {
			if isNativeSidecar(&c) {
				bc := backendContainer{name: c.Name, repo: comparableImageName(c.Image), ports: c.Ports, nativeSidecar: true}
				out[bc.signature()] = bc
			}
		}
	}
	return out
}

// containerRepos collects the image names the Pods run, mesh proxies aside.
// Container order and sidecars don't matter: two workloads that run any image
// in common count as one application.
func containerRepos(pods []*corev1.Pod) map[string]bool {
	out := map[string]bool{}
	for _, pod := range pods {
		for _, c := range pod.Spec.Containers {
			if MeshProxyContainers[c.Name] != "" {
				continue
			}
			if repo := comparableImageName(c.Image); repo != "" {
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
		// The containers that declare the port, by name and image, so a
		// workload missing it is judged by its counterpart of that container
		// when it has one, not by an unrelated sidecar's ports.
		servingNames, servingRepos := map[string]bool{}, map[string]bool{}
		var missing []*backendWorkload
		for _, w := range workloads {
			if !declaresPort(w.containers, port, proto) {
				missing = append(missing, w)
				continue
			}
			gap.Serving = append(gap.Serving, w.Label)
			for _, c := range w.containers {
				if containerDeclaresPort(c, port, proto) {
					servingNames[c.name] = true
					servingRepos[c.repo] = true
				}
			}
		}
		for _, w := range missing {
			candidates := appContainers(w, common)
			var counterparts []backendContainer
			for _, c := range w.containers {
				if servingNames[c.name] || (c.repo != "" && servingRepos[c.repo]) {
					counterparts = append(counterparts, c)
				}
			}
			if len(counterparts) > 0 {
				candidates = counterparts
			}
			for _, c := range candidates {
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
		if containerDeclaresPort(c, port, proto) {
			return true
		}
	}
	return false
}

func containerDeclaresPort(c backendContainer, port int32, proto corev1.Protocol) bool {
	for _, cp := range c.ports {
		if cp.ContainerPort == port && portProtocol(cp.Protocol) == proto {
			return true
		}
	}
	return false
}

// distinctApplications joins workloads that run an image name in common; more than one group means different applications. Sharing anything
// counts, so a sidecar injected into every Pod hides this signal: a missed
// warning costs less than a false one, and the port signal still applies.
func distinctApplications(labels []string, repos []map[string]bool) [][]string {
	parent := make([]int, len(labels))
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
	hasRepo := make([]bool, len(labels))
	for i := range labels {
		for repo := range repos[i] {
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
	for i, label := range labels {
		if !hasRepo[i] {
			continue
		}
		r := find(i)
		if _, ok := groups[r]; !ok {
			roots = append(roots, r)
		}
		groups[r] = append(groups[r], label)
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

// comparableImageName is the image's name, its last path segment without tag
// or digest, lowercased. The registry and the path before the name are left
// out so that the same software pulled through another registry, a mirror, or
// a GCR to Artifact Registry move ("gcr.io/p/web" and
// "us-docker.pkg.dev/p/repo/web") compares equal; two different applications
// both named "web" do too, a rare miss.
func comparableImageName(image string) string {
	repo := strings.ToLower(imageutil.Repository(strings.TrimSpace(image)))
	return repo[strings.LastIndex(repo, "/")+1:]
}

func portProtocol(p corev1.Protocol) corev1.Protocol {
	if p == "" {
		return corev1.ProtocolTCP
	}
	return p
}

// WorkloadAccessNamespace is the namespace to check a caller's access to a
// backend workload in: the Service's, or "" for a cluster-scoped kind (a
// mirror Pod's Node). ok is false when the kind can't be resolved; access
// checks pass such kinds through, so the workload must not be named.
func WorkloadAccessNamespace(w ServiceBackendWorkload, serviceNamespace string) (string, bool) {
	if w.Kind == "Pod" {
		return serviceNamespace, true
	}
	if _, resource := LookupResourceGVR(w.Kind, w.Group); resource == "" {
		return "", false
	}
	if clusterScoped, _, _ := ClassifyKindScope(w.Kind, w.Group); clusterScoped {
		return "", true
	}
	return serviceNamespace, true
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
		namespace, ok := WorkloadAccessNamespace(w, svc.Namespace)
		if !ok {
			return resourcecontext.ServiceBackendGrouping{}
		}
		out.Workloads = append(out.Workloads, resourcecontext.ServiceBackendWorkload{
			ContextRef: resourcecontext.ContextRef{Kind: w.Kind, Group: w.Group, Namespace: namespace, Name: w.Name},
			Pods:       w.Pods,
			Ready:      w.Ready,
		})
	}
	if backends.Mismatched() {
		out.MixedBackends = backends.Problem(svc.Name)
	}
	return out
}
