package k8s

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestParseALBAction(t *testing.T) {
	const forward = `{"Type": "forward","ForwardConfig":{"TargetGroups":[
		{"ServiceName":"app-canary","ServicePort":"80","Weight":0},
		{"ServiceName":"app-stable","ServicePort":80,"Weight":100}
		]}}`

	tests := []struct {
		name        string
		annotations map[string]string
		wantTargets []albActionTarget
		wantFound   bool
		wantBad     bool
	}{
		{name: "absent"},
		{
			name:        "blank is present but unreadable",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": "   "},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "forward with both spellings of servicePort",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": forward},
			wantTargets: []albActionTarget{
				{ServiceName: "app-canary", ServicePort: "80"},
				{ServiceName: "app-stable", ServicePort: "80"},
			},
			wantFound: true,
		},
		{
			name:        "camelCase keys",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"forward","forwardConfig":{"targetGroups":[{"serviceName":"app","servicePort":"http"}]}}`},
			wantTargets: []albActionTarget{{ServiceName: "app", ServicePort: "http"}},
			wantFound:   true,
		},
		{
			name:        "redirect resolves nothing in-cluster",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"Type":"redirect", "RedirectConfig":{"StatusCode":"HTTP_301","Host":"example.test"}}`},
			wantFound:   true,
		},
		{
			name:        "arn-only target group carries no Service",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"Type":"forward","ForwardConfig":{"TargetGroups":[{"TargetGroupARN":"arn:aws:x", "Weight":100}]}}`},
			wantFound:   true,
		},
		{
			name:        "malformed",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"Type":"forward",}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "other backend's annotation does not apply",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.other": forward},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			targets, found, malformed := parseALBAction(tc.annotations, "app")

			if found != tc.wantFound || malformed != tc.wantBad {
				t.Fatalf("found=%v malformed=%v, want %v/%v", found, malformed, tc.wantFound, tc.wantBad)
			}

			if len(targets) != len(tc.wantTargets) {
				t.Fatalf("targets = %+v, want %+v", targets, tc.wantTargets)
			}

			for i, want := range tc.wantTargets {
				if targets[i] != want {
					t.Fatalf("targets[%d] = %+v, want %+v", i, targets[i], want)
				}
			}
		})
	}
}

func albIngress(name string, annotations map[string]string, backends ...string) *networkingv1.Ingress {
	pathType := networkingv1.PathTypeImplementationSpecific
	paths := make([]networkingv1.HTTPIngressPath, 0, len(backends))
	for i, backend := range backends {
		paths = append(paths, networkingv1.HTTPIngressPath{
			Path:     "/" + strings.Repeat("p", i+1) + "/*",
			PathType: &pathType,
			Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
				Name: backend,
				Port: networkingv1.ServiceBackendPort{Name: albActionSentinel},
			}},
		})
	}
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "prod",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-90 * 24 * time.Hour)),
			Annotations:       annotations,
		},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			Host:             "app.example.test",
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: paths}},
		}}},
	}
}

func albService(name string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: port}}},
	}
}

// A use-annotation backend must be resolved through its action annotation
// instead of being reported as a missing Service port.
func TestDetectIngressALBActionBackends(t *testing.T) {
	defer ResetTestState()

	const canary = `{"Type":"forward","ForwardConfig":{"TargetGroups":[
		{"ServiceName":"app-canary","ServicePort":"80","Weight":0},
		{"ServiceName":"app-stable","ServicePort":"80","Weight":100}]}}`

	objects := []runtime.Object{
		albService("app-canary", 80),
		albService("app-stable", 80),
		albService("real", 8080),

		albIngress("canary", map[string]string{ALBActionAnnotation("app"): canary}, "app"),
		albIngress("redirect", map[string]string{ALBActionAnnotation("ssl-redirect"): `{"Type":"redirect","RedirectConfig":{"Protocol":"HTTPS","Port":"443","StatusCode":"HTTP_301"}}`}, "ssl-redirect"),
		albIngress("arn", map[string]string{ALBActionAnnotation("ext"): `{"Type":"forward","ForwardConfig":{"TargetGroups":[{"TargetGroupARN":"arn:aws:x","Weight":100}]}}`}, "ext"),

		albIngress("no-action", nil, "orphan", "orphan"),
		albIngress("bad-action", map[string]string{ALBActionAnnotation("bad"): `{"Type":"forward",}`}, "bad"),
		albIngress("blank-action", map[string]string{ALBActionAnnotation("blank"): " "}, "blank"),
		albIngress("dangling-target", map[string]string{ALBActionAnnotation("fwd"): `{"Type":"forward","ForwardConfig":{"TargetGroups":[{"ServiceName":"gone","ServicePort":"80","Weight":100}]}}`}, "fwd"),
		albIngress("wrong-port", map[string]string{ALBActionAnnotation("fwd"): `{"Type":"forward","ForwardConfig":{"TargetGroups":[{"ServiceName":"real","ServicePort":9090,"Weight":100}]}}`}, "fwd"),
	}
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	problems := detectIngressMissingBackend(GetResourceCache(), "prod", time.Now())

	for _, name := range []string{"canary", "redirect", "arn"} {
		for _, p := range problems {
			if p.Name == name {
				t.Errorf("Ingress %q resolves cleanly through its action annotation but was flagged: %+v", name, p)
			}
		}
	}

	type want struct {
		name, reason, messageSubstr string
	}
	wants := []want{
		{"no-action", "Missing ALB action annotation", ALBActionAnnotation("orphan")},
		{"bad-action", "Invalid ALB action annotation", ALBActionAnnotation("bad")},
		{"blank-action", "Invalid ALB action annotation", ALBActionAnnotation("blank")},
		{"dangling-target", "Missing backend Service", `Service "gone"`},
		{"dangling-target", "Missing backend Service", "via annotation"},
		{"wrong-port", "Missing backend Service port", `port "9090"`},
	}
	for _, w := range wants {
		matched := false
		for _, p := range problems {
			if p.Kind == "Ingress" && p.Namespace == "prod" && p.Name == w.name && p.Reason == w.reason && strings.Contains(p.Message, w.messageSubstr) {
				matched = true
				if p.Severity != "critical" {
					t.Errorf("%s/%s severity = %q, want critical", w.name, w.reason, p.Severity)
				}
			}
		}
		if !matched {
			t.Errorf("missing expected problem %+v\ngot: %+v", w, problems)
		}
	}

	// The rule's Service name is only an annotation key and must never be
	// looked up as a backend. Two paths through one backend share one action.
	count := 0
	for _, p := range problems {
		if p.Name != "no-action" {
			continue
		}
		if p.Reason != "Missing ALB action annotation" {
			t.Errorf("use-annotation backend must not be resolved as a Service: %+v", p)
		}
		count++
	}
	if count != 1 {
		t.Errorf("no-action problems = %d, want exactly one for two paths sharing a backend", count)
	}
}
