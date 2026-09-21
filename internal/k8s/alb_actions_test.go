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
			name:        "simplified forward by ARN",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"forward","targetGroupARN":"arn:aws:x"}`},
			wantFound:   true,
		},
		{
			name:        "simplified forward by name",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"forward","targetGroupName":"legacy"}`},
			wantFound:   true,
		},
		{
			name:        "fixed-response",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"fixed-response","fixedResponseConfig":{"contentType":"text/plain","statusCode":"503"}}`},
			wantFound:   true,
		},
		{
			name:        "malformed JSON",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"Type":"forward",}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "null",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `null`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "empty object",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "missing type",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"forwardConfig":{"targetGroups":[{"serviceName":"app","servicePort":80}]}}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "unknown type",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"proxy","forwardConfig":{"targetGroups":[{"serviceName":"app","servicePort":80}]}}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "type value is case-sensitive",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"Forward","forwardConfig":{"targetGroups":[{"serviceName":"app","servicePort":80}]}}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "forward with no config",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"forward"}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "forward with empty target groups",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"forward","forwardConfig":{"targetGroups":[]}}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "forward with both simplified and advanced schema",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"forward","targetGroupARN":"arn:aws:x","forwardConfig":{"targetGroups":[{"serviceName":"app","servicePort":80}]}}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "target group with neither service nor external group",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"forward","forwardConfig":{"targetGroups":[{"weight":100}]}}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "target group with both service and ARN",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"forward","forwardConfig":{"targetGroups":[{"serviceName":"app","servicePort":80,"targetGroupARN":"arn:aws:x"}]}}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "service target without servicePort",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"forward","forwardConfig":{"targetGroups":[{"serviceName":"app"}]}}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "service target with null servicePort",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"forward","forwardConfig":{"targetGroups":[{"serviceName":"app","servicePort":null}]}}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "service target with empty servicePort",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"forward","forwardConfig":{"targetGroups":[{"serviceName":"app","servicePort":""}]}}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "service target with zero servicePort",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"forward","forwardConfig":{"targetGroups":[{"serviceName":"app","servicePort":0}]}}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "multiple target groups without weights",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"forward","forwardConfig":{"targetGroups":[{"serviceName":"a","servicePort":80},{"serviceName":"b","servicePort":80}]}}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "redirect without statusCode",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"redirect","redirectConfig":{"protocol":"HTTPS","port":"443"}}`},
			wantFound:   true,
			wantBad:     true,
		},
		{
			name:        "fixed-response without config",
			annotations: map[string]string{"alb.ingress.kubernetes.io/actions.app": `{"type":"fixed-response"}`},
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
	ing := sentinelIngress(name, annotations, backends...)
	class := "alb"
	ing.Spec.IngressClassName = &class
	return ing
}

// sentinelIngress builds an Ingress whose every backend uses the use-annotation
// port name, with no IngressClass set.
func sentinelIngress(name string, annotations map[string]string, backends ...string) *networkingv1.Ingress {
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
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "alb"}, Spec: networkingv1.IngressClassSpec{Controller: albIngressController}},
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

// Only the AWS Load Balancer Controller reserves the use-annotation port name.
// Under another controller it is an ordinary named port, and when no controller
// can be identified the backend must be left alone.
func TestDetectIngressSentinelPortByController(t *testing.T) {
	defer ResetTestState()

	nginx := "nginx"
	missingClass := "gone"
	withClass := func(ing *networkingv1.Ingress, class *string) *networkingv1.Ingress {
		ing.Spec.IngressClassName = class
		return ing
	}
	namedPortService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "named", Namespace: "prod"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: albActionSentinel, Port: 80}}},
	}

	objects := []runtime.Object{
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}},
		&networkingv1.IngressClass{
			ObjectMeta: metav1.ObjectMeta{Name: "alb-default", Annotations: map[string]string{"ingressclass.kubernetes.io/is-default-class": "true"}},
			Spec:       networkingv1.IngressClassSpec{Controller: albIngressController},
		},
		namedPortService,
		albService("real", 8080),

		// nginx: use-annotation is a real port name on the Service.
		withClass(sentinelIngress("nginx-named-port", nil, "named"), &nginx),
		// nginx: the Service exists but has no such port.
		withClass(sentinelIngress("nginx-no-port", nil, "real"), &nginx),
		// No class set, cluster default is ALB.
		sentinelIngress("default-class", nil, "orphan"),
		// No class set, legacy annotation names alb.
		sentinelIngress("legacy-alb", map[string]string{"kubernetes.io/ingress.class": "alb"}, "orphan"),
		// No class set, legacy annotation names something else.
		sentinelIngress("legacy-nginx", map[string]string{"kubernetes.io/ingress.class": "nginx"}, "named"),
		// Named class does not exist, alb.* annotation is the only evidence.
		withClass(sentinelIngress("alb-annotation", map[string]string{"alb.ingress.kubernetes.io/scheme": "internet-facing"}, "orphan"), &missingClass),
		// Named class does not exist and nothing else identifies the controller.
		withClass(sentinelIngress("unknown", nil, "orphan"), &missingClass),
		// Named class does not exist. The legacy annotation is not consulted
		// once the field is set, so this is unknown too.
		withClass(sentinelIngress("missing-class-legacy-alb", map[string]string{"kubernetes.io/ingress.class": "alb"}, "orphan"), &missingClass),
		// A resolved nginx class wins over stale alb.* annotations.
		withClass(sentinelIngress("nginx-stale-alb", map[string]string{"alb.ingress.kubernetes.io/scheme": "internet-facing"}, "named"), &nginx),
	}
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	problems := detectIngressMissingBackend(GetResourceCache(), "prod", time.Now())
	reasonsFor := func(name string) []string {
		var out []string
		for _, p := range problems {
			if p.Name == name && p.Reason != "Missing IngressClass" {
				out = append(out, p.Reason)
			}
		}
		return out
	}

	for _, name := range []string{"nginx-named-port", "legacy-nginx", "unknown", "missing-class-legacy-alb", "nginx-stale-alb"} {
		if got := reasonsFor(name); len(got) != 0 {
			t.Errorf("%s: want no backend problems, got %v", name, got)
		}
	}
	for name, want := range map[string]string{
		"nginx-no-port":  "Missing backend Service port",
		"default-class":  "Missing ALB action annotation",
		"legacy-alb":     "Missing ALB action annotation",
		"alb-annotation": "Missing ALB action annotation",
	} {
		got := reasonsFor(name)
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s: want [%s], got %v", name, want, got)
		}
	}
}

// The ssl-redirect and fixed-response idioms usually sit on spec.defaultBackend
// rather than a rule path, so that call site must dispatch the same way.
func TestDetectIngressALBActionDefaultBackend(t *testing.T) {
	defer ResetTestState()

	class := "alb"
	withDefault := func(name, backend string, annotations map[string]string) *networkingv1.Ingress {
		return &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         "prod",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-90 * 24 * time.Hour)),
				Annotations:       annotations,
			},
			Spec: networkingv1.IngressSpec{
				IngressClassName: &class,
				DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
					Name: backend,
					Port: networkingv1.ServiceBackendPort{Name: albActionSentinel},
				}},
			},
		}
	}
	objects := []runtime.Object{
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: class}, Spec: networkingv1.IngressClassSpec{Controller: albIngressController}},
		albService("real", 8080),
		withDefault("redirect", "ssl-redirect", map[string]string{ALBActionAnnotation("ssl-redirect"): `{"type":"redirect","redirectConfig":{"protocol":"HTTPS","port":"443","statusCode":"HTTP_301"}}`}),
		withDefault("no-action", "orphan", nil),
		// The controller requires a usable servicePort alongside serviceName.
		withDefault("no-port", "fwd", map[string]string{ALBActionAnnotation("fwd"): `{"type":"forward","forwardConfig":{"targetGroups":[{"serviceName":"real"}]}}`}),
		withDefault("empty-port", "fwd", map[string]string{ALBActionAnnotation("fwd"): `{"type":"forward","forwardConfig":{"targetGroups":[{"serviceName":"real","servicePort":""}]}}`}),
		withDefault("zero-port", "fwd", map[string]string{ALBActionAnnotation("fwd"): `{"type":"forward","forwardConfig":{"targetGroups":[{"serviceName":"real","servicePort":0}]}}`}),
	}
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	problems := detectIngressMissingBackend(GetResourceCache(), "prod", time.Now())
	for _, p := range problems {
		if p.Name == "redirect" {
			t.Errorf("redirect: want no problems, got %+v", p)
		}
	}
	if !findProblem(problems, "Ingress", "prod", "no-action", "Missing ALB action annotation") {
		t.Errorf("no-action: want Missing ALB action annotation, got %+v", problems)
	}
	for _, p := range problems {
		if p.Name == "no-action" && !strings.HasPrefix(p.Message, "defaultBackend uses port") {
			t.Errorf("no-action: message must name defaultBackend, got %q", p.Message)
		}
	}
	for _, name := range []string{"no-port", "empty-port", "zero-port"} {
		if !findProblem(problems, "Ingress", "prod", name, "Invalid ALB action annotation") {
			t.Errorf("%s: want Invalid ALB action annotation, got %+v", name, problems)
		}
	}
}

// Two default IngressClasses leave the controller ambiguous, so a class-less
// use-annotation backend must not be resolved either way.
func TestDetectIngressSentinelPortAmbiguousDefaultClass(t *testing.T) {
	defer ResetTestState()

	isDefault := map[string]string{"ingressclass.kubernetes.io/is-default-class": "true"}
	objects := []runtime.Object{
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "alb", Annotations: isDefault}, Spec: networkingv1.IngressClassSpec{Controller: albIngressController}},
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx", Annotations: isDefault}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}},
		albService("real", 8080),
		sentinelIngress("ambiguous", nil, "real"),
	}
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	for _, p := range detectIngressMissingBackend(GetResourceCache(), "prod", time.Now()) {
		if p.Name == "ambiguous" && p.Reason != "Missing IngressClass" {
			t.Errorf("ambiguous default class must stay silent, got %+v", p)
		}
	}
}
