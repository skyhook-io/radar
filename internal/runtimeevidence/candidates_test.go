package runtimeevidence

import (
	"context"
	"fmt"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/trace"
	"github.com/skyhook-io/radar/pkg/k8score"
	evidence "github.com/skyhook-io/radar/pkg/runtimeevidence"
	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	kt "k8s.io/client-go/testing"
)

func TestCandidatesConservativeMatching(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*corev1.Pod)
		want int
	}{
		{"supported", func(*corev1.Pod) {}, 1},
		{"wrong image", func(p *corev1.Pod) { p.Spec.Containers[0].Image = "evil/rabbitmq" }, 0},
		{"undeclared", func(p *corev1.Pod) { p.Spec.Containers[0].Ports = nil }, 0},
		{"host network", func(p *corev1.Pod) { p.Spec.HostNetwork = true }, 0},
		{"sidecar port", func(p *corev1.Pod) {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "sidecar", Ports: []corev1.ContainerPort{{ContainerPort: 15692}}})
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testPod()
			tc.edit(p)
			got := CandidatesForPods([]*corev1.Pod{p}, false)
			if len(got.Candidates) != tc.want {
				t.Fatalf("%+v", got)
			}
		})
	}
	var pods []*corev1.Pod
	for i := 0; i < 5; i++ {
		p := testPod()
		p.Name = fmt.Sprintf("rabbit-%d", i)
		pods = append(pods, p)
	}
	got := CandidatesForPods(pods, false)
	if len(got.Candidates) != 3 || !got.Truncated {
		t.Fatalf("%+v", got)
	}
	got = CandidatesForPods(make([]*corev1.Pod, 201), false)
	if !got.CoverageLimited || len(got.Candidates) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestCandidateOwnerUIDAndServiceScope(t *testing.T) {
	yes := true
	ref := func(kind, name, uid string) metav1.OwnerReference {
		return metav1.OwnerReference{APIVersion: "apps/v1", Kind: kind, Name: name, UID: types.UID(uid), Controller: &yes}
	}
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "lab", Name: "rabbit", UID: "deploy-new"}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "lab", Name: "rabbit-rs", UID: "rs-new", OwnerReferences: []metav1.OwnerReference{ref("Deployment", "rabbit", "deploy-new")}}}
	p := testPod()
	p.OwnerReferences = []metav1.OwnerReference{ref("ReplicaSet", "rabbit-rs", "rs-new")}
	p.Labels = map[string]string{"app": "rabbit"}
	stale := p.DeepCopy()
	stale.Name = "stale"
	stale.OwnerReferences[0].UID = "rs-old"
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "lab", Name: "rabbit", UID: "service-id"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "rabbit"}}}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(d, rs, p, stale, svc), ResourceTypes: map[string]bool{"pods": true, "deployments": true, "replicasets": true, "services": true}, DeferredTypes: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Stop()
	deps := trace.Deps{Cache: &k8s.ResourceCache{ResourceCache: core}}
	got := ResolveCandidates(context.Background(), deps, Subject{Kind: "Deployment", Group: "apps", Namespace: "lab", Name: "rabbit"})
	if len(got.Candidates) != 1 || got.Candidates[0].Target.Pod != "rabbit" || got.SubjectUID != "deploy-new" {
		t.Fatalf("UID ownership: %+v", got)
	}
	deps.AllowedNamespaces = []string{}
	got = ResolveCandidates(context.Background(), deps, Subject{Kind: "Deployment", Group: "apps", Namespace: "lab", Name: "rabbit"})
	if len(got.Candidates) != 0 {
		t.Fatal("scope leaked")
	}
	deps.AllowedNamespaces = nil
	got = CandidatesFromTrace(deps, &trace.Trace{Downstream: []trace.Hop{{Resource: Subject{Kind: "Service", Namespace: "lab", Name: "rabbit"}}}})
	if len(got.Candidates) != 2 {
		t.Fatalf("service must include notready selected pods: %+v", got)
	}
	got = ResolveCandidates(context.Background(), deps, Subject{Kind: "Deployment", Group: "other.io", Namespace: "lab", Name: "rabbit"})
	if len(got.Candidates) != 0 {
		t.Fatal("wrong group")
	}
}

func TestNamedAdvisoryPermissions(t *testing.T) {
	for _, allow := range []bool{true, false} {
		client := fake.NewClientset()
		count := 0
		client.PrependReactor("create", "selfsubjectaccessreviews", func(a kt.Action) (bool, runtime.Object, error) {
			r := a.(kt.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
			attrs := r.Spec.ResourceAttributes
			if attrs.Name != "rabbit" || attrs.Namespace != "lab" || attrs.Resource != "pods" || (count == 0 && attrs.Verb != "get") || (count == 1 && (attrs.Verb != "create" || attrs.Subresource != "portforward")) {
				t.Fatalf("wrong exact access check: %+v", attrs)
			}
			count++
			return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: allow}}, nil
		})
		if got := CheckAccess(context.Background(), client, Target{Namespace: "lab", Pod: "rabbit"}); got != allow {
			t.Fatal(got)
		}
		if allow && count != 2 {
			t.Fatal(count)
		}
	}
	if CheckAccess(context.Background(), fake.NewClientset(), Target{Namespace: "lab", Pod: "rabbit"}) {
		t.Fatal("unknown allowed")
	}
}
func TestExplicitStaleTargetNeverOpensTunnel(t *testing.T) {
	c := NewCollector()
	c.start = func(context.Context, kubernetes.Interface, *rest.Config, string, string, int) (tunnel, error) {
		panic("unreachable")
	}
	r := c.CollectTarget(context.Background(), fake.NewClientset(testPod()), &rest.Config{}, evidence.RabbitMQ, Target{Namespace: "lab", Pod: "rabbit", UID: "old"})
	if r.Reason != "target_changed" || r.Facts != nil {
		t.Fatalf("%+v", r)
	}
}
