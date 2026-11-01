package mcp

import (
	"context"
	"github.com/skyhook-io/radar/internal/k8s"
	collector "github.com/skyhook-io/radar/internal/runtimeevidence"
	"github.com/skyhook-io/radar/internal/trace"
	"github.com/skyhook-io/radar/pkg/k8score"
	evidence "github.com/skyhook-io/radar/pkg/runtimeevidence"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
	"testing"
	"time"
)

func TestApplicationHintNeedsDirectReadinessEvidence(t *testing.T) {
	p := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", ReadinessProbe: &corev1.Probe{}}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: false}}}}
	c := collector.Candidate{Application: evidence.Vault, Target: collector.Target{Container: "app"}}
	if applicationActionReason(p, c) == "" {
		t.Fatal("Vault readiness hint absent")
	}
	p.Spec.Containers[0].ReadinessProbe.Exec = &corev1.ExecAction{Command: []string{"rabbitmq-diagnostics", "check_local_alarms"}}
	c.Application = evidence.RabbitMQ
	if applicationActionReason(p, c) == "" {
		t.Fatal("Rabbit readiness hint absent")
	}
	c.Application = evidence.NATS
	if applicationActionReason(p, c) != "" {
		t.Fatal("unsolicited JetStream hint")
	}
	c.Application = evidence.Vault
	p.Status.ContainerStatuses[0].Ready = true
	if applicationActionReason(p, c) != "" {
		t.Fatal("healthy hint")
	}
	p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{Name: "sidecar", Ready: false})
	if applicationActionReason(p, c) != "" {
		t.Fatal("sidecar failure hinted")
	}
	p.Status.ContainerStatuses[0].Ready = false
	p.Spec.Containers[0].ReadinessProbe = nil
	if applicationActionReason(p, c) != "" {
		t.Fatal("no readiness evidence")
	}
}
func TestApplicationHintRestrictedContext(t *testing.T) {
	defer k8s.SetTestLocalMode()()
	t.Setenv("RADAR_CLOUD_MODE", "false")
	ctx := context.WithValue(context.Background(), runtimeLocalCallerKey{}, true)
	if !applicationActionsAllowed(ctx) {
		t.Fatal("eligible local blocked")
	}
	if applicationActionsAllowed(context.WithValue(ctx, applicationActionsDisabledKey{}, true)) {
		t.Fatal("restricted mount suggested unavailable tool")
	}
	if applicationActionsAllowed(context.Background()) {
		t.Fatal("unknown caller allowed")
	}
}
func TestApplicationToolEnum(t *testing.T) {
	s := applicationEvidenceInputSchema()
	got := s.Properties["application"].Enum
	if len(got) != 3 || got[0] != "rabbitmq" || got[1] != "nats" || got[2] != "vault" {
		t.Fatalf("%v", got)
	}
	if s.Properties["adapter"] != nil {
		t.Fatal("stale adapter argument")
	}
}

func TestApplicationActionsAccessAndIdentity(t *testing.T) {
	defer k8s.SetTestLocalMode()()
	t.Setenv("RADAR_CLOUD_MODE", "false")
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "lab", Name: "vault", UID: "current"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "vault", ReadinessProbe: &corev1.Probe{}}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "vault", Ready: false}}}}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(p), ResourceTypes: map[string]bool{"pods": true}, DeferredTypes: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Stop()
	for _, tc := range []struct {
		name                                        string
		allow, stale, restricted, scopeDenied, slow bool
		want                                        int
	}{
		{name: "allowed", allow: true, want: 1}, {name: "denied"}, {name: "unknown"}, {name: "stale", allow: true, stale: true}, {name: "restricted", allow: true, restricted: true}, {name: "namespace", allow: true, scopeDenied: true}, {name: "timeout", allow: true, slow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewClientset()
			calls := 0
			client.PrependReactor("create", "selfsubjectaccessreviews", func(kt.Action) (bool, runtime.Object, error) {
				calls++
				if tc.name == "unknown" {
					return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: true, EvaluationError: "authorizer unavailable"}}, nil
				}
				if tc.slow {
					time.Sleep(510 * time.Millisecond)
				}
				return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: tc.allow}}, nil
			})
			ctx := context.WithValue(context.Background(), runtimeLocalCallerKey{}, true)
			if tc.restricted {
				ctx = context.WithValue(ctx, applicationActionsDisabledKey{}, true)
			}
			deps := trace.Deps{Cache: &k8s.ResourceCache{ResourceCache: core}, Client: client}
			if tc.scopeDenied {
				deps.AllowedNamespaces = []string{}
			}
			c := collector.Candidate{Application: evidence.Vault, Target: collector.Target{Namespace: "lab", Pod: "vault", UID: "current", Container: "vault"}}
			if tc.stale {
				c.Target.UID = "previous"
			}
			got := applicationActions(ctx, deps, collector.CandidateSet{Candidates: []collector.Candidate{c}, CoverageLimited: true})
			if len(got) != tc.want {
				t.Fatalf("actions %+v", got)
			}
			if tc.want == 1 {
				if got[0].Arguments["pod_uid"] != "current" || !got[0].CoverageLimited || got[0].CandidatesTruncated {
					t.Fatalf("coverage/UID %+v", got[0])
				}
				if _, exists := got[0].Arguments["confirm_network_access"]; exists {
					t.Fatal("consent prefilled")
				}
			}
			if (tc.stale || tc.scopeDenied || tc.restricted) && calls != 0 {
				t.Fatal("access preflight for ineligible target")
			}
			for _, a := range client.Actions() {
				if a.GetResource().Resource != "selfsubjectaccessreviews" {
					t.Fatalf("unexpected active request: %+v", a)
				}
			}
		})
	}
}
