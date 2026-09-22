package runtimeevidence

import (
	"context"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/trace"
	evidence "github.com/skyhook-io/radar/pkg/runtimeevidence"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
	for _, p := range pods {
		for _, a := range []evidence.Adapter{evidence.RabbitMQ, evidence.NATS, evidence.Vault} {
			ep, _ := endpointFor(a)
			id, reason := identify(p, ep)
			if reason != "" {
				continue
			}
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
	if canonical == "" || (subject.Group != "" && !k8s.TypedKindOwnsGroup(kind, subject.Group)) {
		return limited
	}
	obj, err := k8s.FetchResource(deps.Cache, kind, subject.Namespace, subject.Name)
	if err != nil {
		return limited
	}
	owner, err := meta.Accessor(obj)
	if err != nil || owner.GetUID() == "" {
		return limited
	}
	pods, err := deps.Cache.Pods().Pods(subject.Namespace).List(labels.Everything())
	if err != nil || len(pods) > maxCandidatePods {
		return limited
	}
	var selected []*corev1.Pod
	for _, p := range pods {
		ref := metav1.GetControllerOf(p)
		if ref == nil {
			continue
		}
		if ref.Kind == canonical && ref.Name == subject.Name && ref.UID == owner.GetUID() && k8s.GroupFromAPIVersion(ref.APIVersion) == subjectGroup(canonical) {
			selected = append(selected, p)
			continue
		}
		if canonical != "Deployment" || ref.Kind != "ReplicaSet" || k8s.GroupFromAPIVersion(ref.APIVersion) != "apps" {
			continue
		}
		if !cacheCovers(deps.Cache, "replicasets", subject.Namespace) || deps.Cache.ReplicaSets() == nil {
			return limited
		}
		rs, err := deps.Cache.ReplicaSets().ReplicaSets(subject.Namespace).Get(ref.Name)
		if err != nil || rs.UID != ref.UID {
			continue
		}
		parent := metav1.GetControllerOf(rs)
		if parent != nil && parent.Kind == canonical && parent.Name == subject.Name && parent.UID == owner.GetUID() && k8s.GroupFromAPIVersion(parent.APIVersion) == "apps" {
			selected = append(selected, p)
		}
	}
	result := CandidatesForPods(selected, false)
	result.SubjectUID = string(owner.GetUID())
	return result
}
func subjectGroup(kind string) string {
	if kind == "Job" || kind == "CronJob" {
		return "batch"
	}
	return "apps"
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
	if client == nil {
		return false
	}
	for _, op := range []struct{ verb, sub string }{{"get", ""}, {"create", "portforward"}} {
		r, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{Namespace: target.Namespace, Verb: op.verb, Resource: "pods", Subresource: op.sub, Name: target.Pod}}}, metav1.CreateOptions{})
		if err != nil || !r.Status.Allowed || r.Status.Denied || r.Status.EvaluationError != "" {
			return false
		}
	}
	return true
}
