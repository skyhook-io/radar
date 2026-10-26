package runtimeevidence

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "lab", Name: "rabbit", UID: "deploy-new"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "rabbit"}}}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "lab", Name: "rabbit-rs", UID: "rs-new", OwnerReferences: []metav1.OwnerReference{ref("Deployment", "rabbit", "deploy-new")}}}
	p := testPod()
	p.OwnerReferences = []metav1.OwnerReference{ref("ReplicaSet", "rabbit-rs", "rs-new")}
	p.Labels = map[string]string{"app": "rabbit"}
	stale := p.DeepCopy()
	stale.Name = "stale"
	stale.OwnerReferences[0].UID = "rs-old"
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "lab", Name: "rabbit", UID: "service-id"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "rabbit"}}}
	objects := []runtime.Object{d, rs, p, stale, svc}
	for i := 0; i < 205; i++ {
		unrelated := testPod()
		unrelated.Name = fmt.Sprintf("unrelated-%d", i)
		unrelated.Labels = map[string]string{"app": "other"}
		objects = append(objects, unrelated)
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(objects...), ResourceTypes: map[string]bool{"pods": true, "deployments": true, "replicasets": true, "services": true}, DeferredTypes: map[string]bool{}})
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
	partial := CandidatesFromTrace(deps, &trace.Trace{Truncated: true, Downstream: []trace.Hop{{Resource: Subject{Kind: "Service", Namespace: "lab", Name: "rabbit"}}}})
	if !partial.CoverageLimited || partial.Truncated {
		t.Fatalf("partial input is not candidate truncation: %+v", partial)
	}
	got = ResolveCandidates(context.Background(), deps, Subject{Kind: "Deployment", Group: "other.io", Namespace: "lab", Name: "rabbit"})
	if len(got.Candidates) != 0 {
		t.Fatal("wrong group")
	}
}

func TestNamedAdvisoryPermissions(t *testing.T) {
	for _, allow := range []bool{true, false} {
		client := fake.NewClientset()
		seen := map[string]int{}
		client.PrependReactor("create", "selfsubjectaccessreviews", func(a kt.Action) (bool, runtime.Object, error) {
			r := a.(kt.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
			attrs := r.Spec.ResourceAttributes
			if attrs.Name != "rabbit" || attrs.Namespace != "lab" || attrs.Resource != "pods" || (attrs.Verb != "get" || attrs.Subresource != "") && (attrs.Verb != "create" || attrs.Subresource != "portforward") {
				t.Fatalf("wrong exact access check: %+v", attrs)
			}
			seen[attrs.Verb+"/"+attrs.Subresource]++
			return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: allow}}, nil
		})
		if got := CheckAccess(context.Background(), client, Target{Namespace: "lab", Pod: "rabbit"}); got != allow {
			t.Fatal(got)
		}
		if seen["get/"] != 1 || seen["create/portforward"] != 1 {
			t.Fatal(seen)
		}
	}
	if CheckAccess(context.Background(), fake.NewClientset(), Target{Namespace: "lab", Pod: "rabbit"}) {
		t.Fatal("unknown allowed")
	}
}

func TestTraceCandidateCoverageForSkippedServices(t *testing.T) {
	p := testPod()
	p.Labels = map[string]string{"app": "rabbit"}
	selected := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "lab", Name: "selected"}, Spec: corev1.ServiceSpec{Selector: p.Labels}}
	selectorless := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "lab", Name: "selectorless"}}
	external := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "lab", Name: "external"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName, ExternalName: "example.com"}}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(p, selected, selectorless, external), ResourceTypes: map[string]bool{"pods": true, "services": true}, DeferredTypes: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Stop()
	deps := trace.Deps{Cache: &k8s.ResourceCache{ResourceCache: core}, AllowedNamespaces: []string{"lab"}}
	for _, tc := range []struct {
		name, namespace string
		limited         bool
	}{
		{"selectorless", "lab", true},
		{"hidden", "other", true},
		{"external", "lab", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CandidatesFromTrace(deps, &trace.Trace{Downstream: []trace.Hop{
				{Resource: Subject{Kind: "Service", Namespace: "lab", Name: "selected"}},
				{Resource: Subject{Kind: "Service", Namespace: tc.namespace, Name: tc.name}},
			}})
			if len(got.Candidates) != 1 || got.CoverageLimited != tc.limited {
				t.Fatalf("%+v", got)
			}
		})
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

func TestRelevantCandidatePrecedesUnrelatedNATSCandidates(t *testing.T) {
	var pods []*corev1.Pod
	for i := 0; i < 4; i++ {
		p := testPod()
		p.Name = fmt.Sprintf("a-nats-%d", i)
		p.UID = types.UID(p.Name)
		p.Spec.Containers[0].Image = "nats:2"
		p.Spec.Containers[0].Ports[0].ContainerPort = 8222
		p.Spec.Containers[0].ReadinessProbe = &corev1.Probe{}
		pods = append(pods, p)
	}
	v := testPod()
	v.Name = "z-vault"
	v.UID = "vault-uid"
	v.Spec.Containers[0].Image = "hashicorp/vault:1"
	v.Spec.Containers[0].Ports[0].ContainerPort = 8200
	v.Spec.Containers[0].ReadinessProbe = &corev1.Probe{}
	pods = append(pods, v)
	got := CandidatesForPods(pods, false)
	if len(got.Candidates) != 3 || got.Candidates[0].Application != evidence.Vault || !got.Truncated {
		t.Fatalf("%+v", got)
	}
}
func TestRabbitHintRequiresAlarmSpecificProbe(t *testing.T) {
	p := testPod()
	p.Spec.Containers[0].ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"rabbitmq-diagnostics", "-q", "ping"}}}}
	if RelevantReadinessFailure(p, evidence.RabbitMQ, "rabbit") {
		t.Fatal("generic readiness suggested alarm check")
	}
	p.Spec.Containers[0].ReadinessProbe.Exec.Command = []string{"gosu", "rabbitmq", "rabbitmq-diagnostics", "-q", "check_local_alarms"}
	if !RelevantReadinessFailure(p, evidence.RabbitMQ, "rabbit") {
		t.Fatal("alarm-specific readiness omitted")
	}
}

func TestAccessResultSeparatesDenialFromUnavailableEvaluation(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		status                 authv1.SubjectAccessReviewStatus
		err                    error
		wantAllowed, wantError bool
	}{
		{name: "allowed", status: authv1.SubjectAccessReviewStatus{Allowed: true}, wantAllowed: true},
		{name: "denied", status: authv1.SubjectAccessReviewStatus{Denied: true}},
		{name: "not allowed"},
		{name: "transport", err: errors.New("connection unavailable"), wantError: true},
		{name: "evaluation", status: authv1.SubjectAccessReviewStatus{Allowed: true, EvaluationError: "authorizer unavailable"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewClientset()
			client.PrependReactor("create", "selfsubjectaccessreviews", func(kt.Action) (bool, runtime.Object, error) {
				return true, &authv1.SelfSubjectAccessReview{Status: tc.status}, tc.err
			})
			allowed, err := CheckAccessResult(context.Background(), client, Target{Namespace: "lab", Pod: "rabbit"})
			if allowed != tc.wantAllowed || (err != nil) != tc.wantError {
				t.Fatalf("allowed=%v error=%v", allowed, err)
			}
			if got := CheckAccess(context.Background(), client, Target{Namespace: "lab", Pod: "rabbit"}); got != tc.wantAllowed {
				t.Fatalf("boolean caller changed fail-closed behavior: %v", got)
			}
		})
	}
}

func TestNamedPermissionChecksShareOneConcurrentBudget(t *testing.T) {
	var arrived atomic.Int32
	ready := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if arrived.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"authorization.k8s.io/v1","kind":"SelfSubjectAccessReview","status":{"allowed":true}}`))
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if allowed, err := CheckAccessResult(ctx, client, Target{Namespace: "lab", Pod: "rabbit"}); !allowed || err != nil {
		t.Fatalf("checks did not run concurrently: allowed=%v error=%v", allowed, err)
	}
}

func TestDefinitivePermissionDenialDoesNotAskForRetry(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("create", "selfsubjectaccessreviews", func(a kt.Action) (bool, runtime.Object, error) {
		r := a.(kt.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		if r.Spec.ResourceAttributes.Verb == "get" {
			return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Denied: true, EvaluationError: "another authorizer unavailable"}}, nil
		}
		return true, nil, errors.New("transport unavailable")
	})
	if allowed, err := CheckAccessResult(context.Background(), client, Target{Namespace: "lab", Pod: "rabbit"}); allowed || err != nil {
		t.Fatalf("a definitive denial must stay denied without retry: allowed=%v error=%v", allowed, err)
	}
}

func TestCompletedPermissionResponsesAreNotDiscardedByLaterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := fake.NewClientset()
	var completed atomic.Int32
	client.PrependReactor("create", "selfsubjectaccessreviews", func(kt.Action) (bool, runtime.Object, error) {
		if completed.Add(1) == 2 {
			cancel()
		}
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	if allowed, err := CheckAccessResult(ctx, client, Target{Namespace: "lab", Pod: "rabbit"}); !allowed || err != nil {
		t.Fatalf("completed responses were discarded: allowed=%v error=%v", allowed, err)
	}
}
