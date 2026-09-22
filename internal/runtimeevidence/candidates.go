package runtimeevidence

import (
	"context"
	"path"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/trace"
	evidence "github.com/skyhook-io/radar/pkg/runtimeevidence"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
)

const MaxCandidates = 3
const maxCandidatePods = 200

type Subject = trace.ResourceRef

type Candidate struct {
	Application      evidence.Adapter `json:"application"`
	Target           Target           `json:"target"`
	ExpectedEvidence []string         `json:"expectedEvidence"`
	Coverage         string           `json:"coverage"`
}
type CandidateSet struct {
	SubjectUID      string      `json:"subjectUID,omitempty"`
	Candidates      []Candidate `json:"candidates"`
	Truncated       bool        `json:"truncated,omitempty"`
	CoverageLimited bool        `json:"coverageLimited,omitempty"`
}

func CandidatesForPods(pods []*corev1.Pod, truncated bool) CandidateSet {
	result := CandidateSet{Candidates: []Candidate{}, Truncated: truncated}
	if len(pods) > maxCandidatePods {
		result.CoverageLimited = true
		return result
	}
	readiness := map[string]bool{}
	for _, p := range pods {
		for _, a := range []evidence.Adapter{evidence.RabbitMQ, evidence.NATS, evidence.Vault} {
			ep, _ := endpointFor(a)
			id, reason := identify(p, ep)
			if reason != "" {
				continue
			}
			readiness[id.uid+"/"+id.name] = RelevantReadinessFailure(p, a, id.name)
			facts := []string{"initialized", "sealed", "standby"}
			if a == evidence.RabbitMQ {
				facts = []string{"node disk alarm", "node memory alarm"}
			}
			if a == evidence.NATS {
				facts = []string{"JetStream consumer counts", "reported stream/account coverage"}
			}
			result.Candidates = append(result.Candidates, Candidate{Application: a, Target: Target{Namespace: p.Namespace, Pod: p.Name, UID: id.uid, Container: id.name}, ExpectedEvidence: facts, Coverage: "Selected Pod endpoint only; other replicas and client network paths are not covered."})
		}
	}
	sort.Slice(result.Candidates, func(i, j int) bool {
		a, b := result.Candidates[i], result.Candidates[j]
		if readiness[a.Target.UID+"/"+a.Target.Container] != readiness[b.Target.UID+"/"+b.Target.Container] {
			return readiness[a.Target.UID+"/"+a.Target.Container]
		}
		return a.Target.Namespace+"/"+a.Target.Pod+"/"+string(a.Application) < b.Target.Namespace+"/"+b.Target.Pod+"/"+string(b.Application)
	})
	if len(result.Candidates) > MaxCandidates {
		result.Candidates = result.Candidates[:MaxCandidates]
		result.Truncated = true
	}
	return result
}

func cacheCovers(cache *k8s.ResourceCache, kind, ns string) bool {
	if cache == nil || cache.ResourceCache == nil || !cache.KindCoversNamespace(kind, ns) {
		return false
	}
	synced, known := cache.InformerSynced(kind)
	return known && synced
}

func ResolveCandidates(ctx context.Context, deps trace.Deps, subject Subject) CandidateSet {
	limited := CandidateSet{Candidates: []Candidate{}, CoverageLimited: true}
	if !deps.NamespaceAllowed(subject.Namespace) || !cacheCovers(deps.Cache, "pods", subject.Namespace) || deps.Cache.Pods() == nil {
		return limited
	}
	kind := strings.ToLower(subject.Kind)
	switch kind {
	case "service", "ingress", "gateway", "httproute", "grpcroute":
		group := ""
		canonical := "Service"
		switch kind {
		case "ingress":
			group = "networking.k8s.io"
			canonical = "Ingress"
		case "gateway":
			group = "gateway.networking.k8s.io"
			canonical = "Gateway"
		case "httproute":
			group = "gateway.networking.k8s.io"
			canonical = "HTTPRoute"
		case "grpcroute":
			group = "gateway.networking.k8s.io"
			canonical = "GRPCRoute"
		}
		if subject.Group != "" && subject.Group != group {
			return limited
		}
		tr, err := trace.BuildTraceWithOptions(ctx, deps, canonical, subject.Namespace, subject.Name, trace.Options{})
		if err != nil {
			return limited
		}
		result := CandidatesFromTrace(deps, tr)
		if group == "gateway.networking.k8s.io" && deps.Dynamic != nil && deps.Discovery != nil {
			if gvr, ok := deps.Discovery.GetGVRWithGroup(canonical, group); ok {
				if obj, err := deps.Dynamic.GetWatched(gvr, subject.Namespace, subject.Name); err == nil {
					result.SubjectUID = string(obj.GetUID())
				}
			}
		}
		if obj, err := k8s.FetchResource(deps.Cache, kind, subject.Namespace, subject.Name); err == nil {
			if m, err := meta.Accessor(obj); err == nil {
				result.SubjectUID = string(m.GetUID())
			}
		}
		return result
	}
	if kind == "pod" {
		if subject.Group != "" {
			return limited
		}
		p, err := deps.Cache.Pods().Pods(subject.Namespace).Get(subject.Name)
		if err != nil {
			return limited
		}
		result := CandidatesForPods([]*corev1.Pod{p}, false)
		result.SubjectUID = string(p.UID)
		return result
	}
	canonical := k8s.CanonicalWorkloadKind(kind)
	if canonical == "" {
		return limited
	}
	var obj runtime.Object
	var selector *metav1.LabelSelector
	var err error
	if canonical == "Rollout" {
		if (subject.Group != "" && subject.Group != "argoproj.io") || deps.Dynamic == nil || deps.Discovery == nil {
			return limited
		}
		gvr, ok := deps.Discovery.GetGVRWithGroup("Rollout", "argoproj.io")
		if !ok {
			return limited
		}
		obj, err = deps.Dynamic.GetWatched(gvr, subject.Namespace, subject.Name)
		if err != nil {
			return limited
		}
		selector, err = k8s.ResolveRolloutSelector(deps.Cache, obj.(*unstructured.Unstructured))
	} else {
		if subject.Group != "" && !k8s.TypedKindOwnsGroup(kind, subject.Group) {
			return limited
		}
		obj, err = k8s.FetchResource(deps.Cache, kind, subject.Namespace, subject.Name)
		if err != nil {
			return limited
		}
		selector, err = k8s.GetWorkloadSelector(deps.Cache, kind, subject.Namespace, subject.Name)
	}
	if err != nil || selector == nil {
		return limited
	}
	owner, err := meta.Accessor(obj)
	if err != nil || owner.GetUID() == "" {
		return limited
	}
	parsed, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return limited
	}
	selected, truncated, err := k8s.WorkloadPodsWithOptions(deps.Cache, kind, subject.Namespace, subject.Name, k8s.WorkloadPodOptions{OwnerUID: owner.GetUID(), Selector: parsed, MaxCandidates: maxCandidatePods})
	if err != nil {
		return limited
	}
	result := CandidatesForPods(selected, truncated)
	result.CoverageLimited = truncated
	result.SubjectUID = string(owner.GetUID())
	return result
}

func CandidatesFromTrace(deps trace.Deps, tr *trace.Trace) CandidateSet {
	limited := CandidateSet{Candidates: []Candidate{}, CoverageLimited: true}
	if tr == nil || len(tr.Downstream) > maxCandidatePods || deps.Cache == nil || deps.Cache.Pods() == nil || deps.Cache.Services() == nil {
		return limited
	}
	var pods []*corev1.Pod
	seen := map[string]bool{}
	partial := tr.Truncated
	for _, hop := range tr.Downstream {
		ref := hop.Resource
		if ref.Kind != "Service" || ref.Group != "" || !deps.NamespaceAllowed(ref.Namespace) {
			continue
		}
		key := ref.Namespace + "/" + ref.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		if !cacheCovers(deps.Cache, "pods", ref.Namespace) || !cacheCovers(deps.Cache, "services", ref.Namespace) {
			partial = true
			continue
		}
		svc, err := deps.Cache.Services().Services(ref.Namespace).Get(ref.Name)
		if err != nil {
			partial = true
			continue
		}
		if svc.Spec.Type == corev1.ServiceTypeExternalName || len(svc.Spec.Selector) == 0 {
			continue
		}
		selected, err := deps.Cache.Pods().Pods(ref.Namespace).List(labels.SelectorFromSet(labels.Set(svc.Spec.Selector)))
		if err != nil {
			partial = true
			continue
		}
		if len(pods)+len(selected) > maxCandidatePods {
			return limited
		}
		for _, p := range selected {
			key := "pod/" + p.Namespace + "/" + p.Name
			if !seen[key] {
				seen[key] = true
				pods = append(pods, p)
			}
		}
	}
	result := CandidatesForPods(pods, partial)
	result.CoverageLimited = partial
	return result
}

func CheckAccess(ctx context.Context, client kubernetes.Interface, target Target) bool {
	if client == nil || ctx.Err() != nil {
		return false
	}
	for _, op := range []struct{ verb, sub string }{{"get", ""}, {"create", "portforward"}} {
		r, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{Namespace: target.Namespace, Verb: op.verb, Resource: "pods", Subresource: op.sub, Name: target.Pod}}}, metav1.CreateOptions{})
		if err != nil || ctx.Err() != nil || !r.Status.Allowed || r.Status.Denied || r.Status.EvaluationError != "" {
			return false
		}
	}
	return true
}

func ReadinessFailure(p *corev1.Pod, containerName string) bool {
	if p == nil {
		return false
	}
	for _, c := range p.Spec.Containers {
		if c.Name != containerName || c.ReadinessProbe == nil {
			continue
		}
		for _, status := range p.Status.ContainerStatuses {
			if status.Name == containerName {
				return !status.Ready
			}
		}
	}
	return false
}

func RelevantReadinessFailure(p *corev1.Pod, application evidence.Adapter, containerName string) bool {
	if !ReadinessFailure(p, containerName) {
		return false
	}
	if application == evidence.Vault {
		return true
	}
	if application != evidence.RabbitMQ {
		return false
	}
	for _, c := range p.Spec.Containers {
		if c.Name != containerName || c.ReadinessProbe == nil || c.ReadinessProbe.Exec == nil {
			continue
		}
		cmd := c.ReadinessProbe.Exec.Command
		if len(cmd) >= 3 && path.Base(cmd[0]) == "gosu" {
			cmd = cmd[2:]
		}
		if len(cmd) == 0 || path.Base(cmd[0]) != "rabbitmq-diagnostics" {
			continue
		}
		for _, arg := range cmd[1:] {
			if arg == "check_local_alarms" || arg == "check_alarms" {
				return true
			}
		}
	}
	return false
}
