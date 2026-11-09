package k8s

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func argoRolloutScaled(name, ns string, replicas int64, tmplLabels map[string]string) *unstructured.Unstructured {
	lbls := map[string]any{}
	for k, v := range tmplLabels {
		lbls[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Rollout",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{
			// float64 is the shape a JSON-decoded dynamic-informer object carries in
			// production; seed it here so the tests exercise the real decode path.
			"replicas": float64(replicas),
			"template": map[string]any{"metadata": map[string]any{"labels": lbls}},
		},
	}}
}

// withRollout sets up the typed + dynamic caches with one Service and one Argo
// Rollout, returning the Service. The caller asserts on scaledToZeroBackingWorkload.
func withRollout(t *testing.T, replicas int64) *corev1.Service {
	t.Helper()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "api"}},
	}
	if err := InitTestResourceCache(fake.NewClientset(svc)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	rolloutGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{rolloutGVR: "RolloutList"},
		argoRolloutScaled("api", "prod", replicas, map[string]string{"app": "api"}),
	)
	if err := InitTestDynamicResourceCache(dynClient, []APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout", Name: "rollouts", Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	dynCache := GetDynamicResourceCache()
	if err := dynCache.EnsureWatching(rolloutGVR); err != nil {
		t.Fatalf("EnsureWatching rollouts: %v", err)
	}
	if !dynCache.WaitForSync(rolloutGVR, 2*time.Second) {
		t.Fatal("rollout dynamic cache did not sync")
	}
	return svc
}

// TestScaledToZero_RolloutAtZeroIsSoftened: an Argo Rollout scaled to 0 is the
// same deliberate-dormancy case as a Deployment at 0 - recognized so the trace
// reads degraded/benign, not red "broken".
func TestScaledToZero_RolloutAtZeroIsSoftened(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	svc := withRollout(t, 0)
	if zero, _ := scaledToZeroBackingWorkload(GetResourceCache(), svc); !zero {
		t.Error("Rollout at replicas=0 matching the selector should be recognized as scaled-to-zero")
	}
}

// TestScaledToZero_RolloutAboveZeroStaysCritical: a Rollout at replicas>0 with no
// ready pods is a REAL break (the workload wants pods but has none) - the guard
// must not soften it just because Rollouts can scale to 0.
func TestScaledToZero_RolloutAboveZeroStaysCritical(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	svc := withRollout(t, 2)
	if zero, _ := scaledToZeroBackingWorkload(GetResourceCache(), svc); zero {
		t.Error("Rollout at replicas>0 must NOT be softened - 0 ready under replicas>0 is a real break")
	}
}

// TestScaledToZero_NoRolloutCRDIsNotABreak: when the Rollout CRD isn't installed,
// the dynamic lookup returns ErrUnknownDynamicKind; absence must read as "no
// match", never an error that flips the result.
func TestScaledToZero_NoRolloutCRDIsNotABreak(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "api"}},
	}
	if err := InitTestResourceCache(fake.NewClientset(svc)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	if zero, outcome := scaledToZeroBackingWorkload(GetResourceCache(), svc); zero || outcome != rolloutLookupConclusive {
		t.Error("with no backing workload and no Rollout CRD, must not report scaled-to-zero or uncertain")
	}
}

// TestNoReadyEndpointsTruthTable pins the full severity matrix for a Service
// whose selector matches pods (selected=1) with 0 ready. The observation
// "0/N selected pods ready" is certain in every row; severity encodes how
// confidently it explains an outage:
//
//	Deploy/STS scaled to 0        → warning  (deliberate dormancy)
//	Rollout scaled to 0           → warning  (deliberate dormancy)
//	Rollout unreadable (RBAC)     → critical + explicit caveat (gap never closes)
//	Rollout cache not yet synced  → warning  "re-check shortly" (self-heals next poll)
//	no workload at 0              → critical, no caveat (confirmed outage)
//	publishNotReadyAddresses      → info     (traffic routes to not-ready pods by design)
func TestNoReadyEndpointsTruthTable(t *testing.T) {
	rolloutGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	rolloutAPIResource := []APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout", Name: "rollouts", Namespaced: true, Verbs: []string{"list", "watch"}},
	}

	cases := []struct {
		name            string
		pnr             bool
		typed           func(old metav1.Time) []runtime.Object
		dynamic         string // "", "zero", "forbidden", "transient"
		wantSeverity    string
		wantReason      string
		wantFingerprint string
		wantMsgSub      string
		wantEmptyMsg    bool
	}{
		{
			name: "deployment scaled to zero",
			typed: func(old metav1.Time) []runtime.Object {
				zero := int32(0)
				return []runtime.Object{&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", CreationTimestamp: old},
					Spec: appsv1.DeploymentSpec{
						Replicas: &zero,
						Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}}},
					},
				}}
			},
			wantSeverity:    "warning",
			wantReason:      ScaledToZeroReason,
			wantFingerprint: ScaledToZeroFingerprint,
		},
		{
			name: "statefulset scaled to zero",
			typed: func(old metav1.Time) []runtime.Object {
				zero := int32(0)
				return []runtime.Object{&appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", CreationTimestamp: old},
					Spec: appsv1.StatefulSetSpec{
						Replicas: &zero,
						Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}}},
					},
				}}
			},
			wantSeverity:    "warning",
			wantReason:      ScaledToZeroReason,
			wantFingerprint: ScaledToZeroFingerprint,
		},
		{
			name:            "rollout scaled to zero",
			dynamic:         "zero",
			wantSeverity:    "warning",
			wantReason:      ScaledToZeroReason,
			wantFingerprint: ScaledToZeroFingerprint,
		},
		{
			name:            "rollout unreadable forbidden stays critical with caveat",
			dynamic:         "forbidden",
			wantSeverity:    "critical",
			wantReason:      "0/1 selected pods ready",
			wantFingerprint: "svc:no-ready-endpoints",
			wantMsgSub:      "no RBAC",
		},
		{
			name:            "rollout cache not synced softens to warning",
			dynamic:         "transient",
			wantSeverity:    "warning",
			wantReason:      "0/1 selected pods ready",
			wantFingerprint: "svc:no-ready-endpoints",
			wantMsgSub:      "re-check shortly",
		},
		{
			name:            "no workload is a confirmed outage",
			wantSeverity:    "critical",
			wantReason:      "0/1 selected pods ready",
			wantFingerprint: "svc:no-ready-endpoints",
			wantEmptyMsg:    true,
		},
		{
			name:            "publishNotReadyAddresses is informational",
			pnr:             true,
			wantSeverity:    "info",
			wantReason:      "0/1 selected pods ready",
			wantFingerprint: "svc:no-ready-endpoints",
			wantMsgSub:      "publishNotReadyAddresses",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer ResetTestState()
			defer ResetTestDynamicState()

			old := metav1.NewTime(time.Now().Add(-30 * time.Minute))
			objs := []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "api-0", Namespace: "prod", Labels: map[string]string{"app": "api"}, CreationTimestamp: old},
					Status: corev1.PodStatus{
						Phase:      corev1.PodRunning,
						Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
					},
				},
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", CreationTimestamp: old},
					Spec: corev1.ServiceSpec{
						Selector:                 map[string]string{"app": "api"},
						Ports:                    []corev1.ServicePort{{Port: 80}},
						PublishNotReadyAddresses: tc.pnr,
					},
				},
			}
			if tc.typed != nil {
				objs = append(objs, tc.typed(old)...)
			}
			if err := InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
				t.Fatalf("InitTestResourceCache: %v", err)
			}

			switch tc.dynamic {
			case "zero":
				dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
					runtime.NewScheme(),
					map[schema.GroupVersionResource]string{rolloutGVR: "RolloutList"},
					argoRolloutScaled("api", "prod", 0, map[string]string{"app": "api"}),
				)
				if err := InitTestDynamicResourceCache(dynClient, rolloutAPIResource); err != nil {
					t.Fatalf("InitTestDynamicResourceCache: %v", err)
				}
				dynCache := GetDynamicResourceCache()
				if err := dynCache.EnsureWatching(rolloutGVR); err != nil {
					t.Fatalf("EnsureWatching rollouts: %v", err)
				}
				if !dynCache.WaitForSync(rolloutGVR, 2*time.Second) {
					t.Fatal("rollout dynamic cache did not sync")
				}
			case "forbidden":
				dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
					runtime.NewScheme(),
					map[schema.GroupVersionResource]string{rolloutGVR: "RolloutList"},
				)
				dynClient.PrependReactor("list", "rollouts", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "argoproj.io", Resource: "rollouts"}, "", errors.New("no list permission"))
				})
				if err := InitTestDynamicResourceCache(dynClient, rolloutAPIResource); err != nil {
					t.Fatalf("InitTestDynamicResourceCache: %v", err)
				}
			case "transient":
				// A non-auth list failure: the probe fails open, the informer
				// starts but its backing list keeps erroring, so the cache
				// never syncs — the persistent "not synced yet" shape.
				dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
					runtime.NewScheme(),
					map[schema.GroupVersionResource]string{rolloutGVR: "RolloutList"},
				)
				dynClient.PrependReactor("list", "rollouts", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewInternalError(errors.New("etcd hiccup"))
				})
				if err := InitTestDynamicResourceCache(dynClient, rolloutAPIResource); err != nil {
					t.Fatalf("InitTestDynamicResourceCache: %v", err)
				}
			}

			deadline := time.Now().Add(2 * time.Second)
			var got *Detection
			var problems []Detection
			for time.Now().Before(deadline) {
				problems = DetectProblems(GetResourceCache(), "prod")
				for i := range problems {
					p := problems[i]
					if p.Kind == "Service" && p.Name == "api" && p.Reason == tc.wantReason && p.Severity == tc.wantSeverity {
						got = &problems[i]
						break
					}
				}
				if got != nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if got == nil {
				t.Fatalf("expected Service/api detection reason=%q severity=%q, got: %+v", tc.wantReason, tc.wantSeverity, problems)
			}
			if got.Fingerprint != tc.wantFingerprint {
				t.Errorf("fingerprint = %q, want %q", got.Fingerprint, tc.wantFingerprint)
			}
			if tc.wantMsgSub != "" && !strings.Contains(got.Message, tc.wantMsgSub) {
				t.Errorf("message %q should contain %q", got.Message, tc.wantMsgSub)
			}
			if tc.wantEmptyMsg && got.Message != "" {
				t.Errorf("confirmed outage must carry no caveat message, got %q", got.Message)
			}
			if !got.OnsetUnknown || got.Duration != "" || got.DurationSeconds != 0 {
				t.Errorf("Service backend snapshot must preserve age but omit invented onset: %+v", got)
			}
			// The matched row must be the ONLY Service/api endpoint-health row
			// in that snapshot — a benign framing must not coexist with a red
			// outage row for the same observation.
			for _, p := range problems {
				if p.Kind == "Service" && p.Name == "api" && p.Fingerprint != tc.wantFingerprint &&
					(p.Fingerprint == "svc:no-ready-endpoints" || p.Fingerprint == ScaledToZeroFingerprint) {
					t.Errorf("conflicting endpoint-health row for the same Service: %+v", p)
				}
			}
		})
	}
}

// pollScaledToZero re-evaluates scaledToZeroBackingWorkload until the predicate
// holds or the deadline passes, absorbing standalone-cache informer sync latency.
func pollScaledToZero(t *testing.T, cache *ResourceCache, svc *corev1.Service, ok func(bool, rolloutLookupOutcome) bool) (bool, rolloutLookupOutcome) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var zero bool
	var outcome rolloutLookupOutcome
	for time.Now().Before(deadline) {
		zero, outcome = scaledToZeroBackingWorkload(cache, svc)
		if ok(zero, outcome) {
			return zero, outcome
		}
		time.Sleep(20 * time.Millisecond)
	}
	return zero, outcome
}

func scaleZeroSvc(ns string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: ns},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "api"}},
	}
}

func scaledZeroDeployment(ns string) *appsv1.Deployment {
	zero := int32(0)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: &zero,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}}},
		},
	}
}

// TestScaledToZero_UncoveredScopeStaysUnverifiable: on a namespace-restricted
// install the typed Deployment/StatefulSet informers don't watch the Service's
// namespace, so their empty List can't rule out an intentionally scaled-to-zero
// backing workload. The outcome must be scope-unverifiable (drives a warning),
// NOT a confident conclusive that would let the ready==0 branch emit a false
// critical - EVEN when a scaled-to-zero workload really does exist there.
func TestScaledToZero_UncoveredScopeStaysUnverifiable(t *testing.T) {
	ResetTestDynamicState()
	svc := scaleZeroSvc("staging")
	cache := scopedRefTestCache(t, fake.NewClientset(svc, scaledZeroDeployment("staging")),
		map[string]k8score.ResourceScope{
			"services":     {Enabled: true},
			"deployments":  {Enabled: true, Namespace: "prod"},
			"statefulsets": {Enabled: true, Namespace: "prod"},
		})
	zero, outcome := pollScaledToZero(t, cache, svc, func(_ bool, o rolloutLookupOutcome) bool {
		return o == rolloutLookupScopeUnverifiable
	})
	if zero {
		t.Fatal("prod-scoped informer can't observe the staging Deployment; must not claim scaled-to-zero")
	}
	if outcome != rolloutLookupScopeUnverifiable {
		t.Fatalf("uncovered workload scope must be unverifiable, got %v", outcome)
	}
}

// TestScaledToZero_CoveredWorkloadStillPositive: when the informers DO cover the
// namespace, a real scaled-to-zero Deployment is recognized (positive, benign)
// and the outcome is conclusive - the coverage gate must not suppress genuine
// detection.
func TestScaledToZero_CoveredWorkloadStillPositive(t *testing.T) {
	ResetTestDynamicState()
	svc := scaleZeroSvc("prod")
	cache := scopedRefTestCache(t, fake.NewClientset(svc, scaledZeroDeployment("prod")),
		map[string]k8score.ResourceScope{
			"services":     {Enabled: true},
			"deployments":  {Enabled: true},
			"statefulsets": {Enabled: true},
		})
	zero, outcome := pollScaledToZero(t, cache, svc, func(z bool, _ rolloutLookupOutcome) bool { return z })
	if !zero {
		t.Fatal("covered scaled-to-zero Deployment must be recognized as benign")
	}
	if outcome != rolloutLookupConclusive {
		t.Fatalf("covered positive outcome = %v, want conclusive", outcome)
	}
}

// TestScaledToZero_CoveredNoWorkloadIsConclusive: covered namespace with no
// backing workload at all is a confident conclusive - the ready==0 branch then
// legitimately emits a critical. The coverage gate must not soften this real
// outage into an uncertain warning.
func TestScaledToZero_CoveredNoWorkloadIsConclusive(t *testing.T) {
	ResetTestDynamicState()
	svc := scaleZeroSvc("prod")
	cache := scopedRefTestCache(t, fake.NewClientset(svc),
		map[string]k8score.ResourceScope{
			"services":     {Enabled: true},
			"deployments":  {Enabled: true},
			"statefulsets": {Enabled: true},
		})
	zero, outcome := scaledToZeroBackingWorkload(cache, svc)
	if zero {
		t.Fatal("no backing workload must not report scaled-to-zero")
	}
	if outcome != rolloutLookupConclusive {
		t.Fatalf("covered no-workload outcome = %v, want conclusive", outcome)
	}
}

// TestPublishNotReadyDoesNotSuppressUnresolvedTargetPort: publishNotReadyAddresses
// only reframes READINESS - it cannot make a misspelled named targetPort route
// anywhere. Both rows must coexist: the info row for 0 ready (benign under PNR)
// AND the high-severity unresolved-targetport row (a real break the PNR framing
// must not swallow).
func TestPublishNotReadyDoesNotSuppressUnresolvedTargetPort(t *testing.T) {
	defer ResetTestState()

	old := metav1.NewTime(time.Now().Add(-30 * time.Minute))
	objs := []runtime.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "api-0", Namespace: "prod", Labels: map[string]string{"app": "api"}, CreationTimestamp: old},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:  "api",
				Ports: []corev1.ContainerPort{{Name: "web", ContainerPort: 8080}},
			}}},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "api"},
				// "wep" is a misspelling of the pod's "web" port: it resolves to
				// no container port, so nothing routes regardless of PNR.
				Ports:                    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromString("wep")}},
				PublishNotReadyAddresses: true,
			},
		},
	}
	if err := InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	var infoRow, targetPortRow *Detection
	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(GetResourceCache(), "prod")
		infoRow, targetPortRow = nil, nil
		for i := range problems {
			p := &problems[i]
			if p.Kind != "Service" || p.Name != "api" {
				continue
			}
			switch p.Fingerprint {
			case "svc:no-ready-endpoints":
				infoRow = p
			case "svc:unresolved-targetport":
				targetPortRow = p
			}
		}
		if infoRow != nil && targetPortRow != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if infoRow == nil {
		t.Fatalf("missing the publishNotReadyAddresses info row; got: %+v", problems)
	}
	if infoRow.Severity != "info" || !strings.Contains(infoRow.Message, "publishNotReadyAddresses") {
		t.Errorf("PNR row must stay informational and name publishNotReadyAddresses, got severity=%q message=%q", infoRow.Severity, infoRow.Message)
	}
	if targetPortRow == nil {
		t.Fatalf("PNR must not suppress the unresolved named targetPort detection; got: %+v", problems)
	}
	if targetPortRow.Severity != "high" || !strings.Contains(targetPortRow.Reason, "wep") {
		t.Errorf("unresolved-targetport row must stay high severity and name the port, got severity=%q reason=%q", targetPortRow.Severity, targetPortRow.Reason)
	}
}

func TestNestedNumberInt64(t *testing.T) {
	obj := func(v any) map[string]any { return map[string]any{"spec": map[string]any{"replicas": v}} }
	cases := []struct {
		name   string
		in     any
		want   int64
		wantOK bool
	}{
		{"int64", int64(0), 0, true},
		{"int64 nonzero", int64(3), 3, true},
		{"float64 integral (production shape)", float64(0), 0, true},
		{"float64 integral nonzero", float64(3), 3, true},
		{"int32", int32(2), 2, true},
		{"int", int(5), 5, true},
		{"float64 fractional", float64(1.5), 0, false},
		{"string", "3", 0, false},
	}
	for _, c := range cases {
		got, ok := nestedNumberInt64(obj(c.in), "spec", "replicas")
		if got != c.want || ok != c.wantOK {
			t.Errorf("%s: nestedNumberInt64 = (%d,%v), want (%d,%v)", c.name, got, ok, c.want, c.wantOK)
		}
	}
	if _, ok := nestedNumberInt64(map[string]any{"spec": map[string]any{}}, "spec", "replicas"); ok {
		t.Error("missing field must report not-found")
	}
}

// --- Scan every matching workload, including workloadRef Rollouts ---

func deploymentWithReplicas(name, ns string, replicas int32, lbls map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: lbls}},
		},
	}
}

func statefulSetWithReplicas(name, ns string, replicas int32, lbls map[string]string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: lbls}},
		},
	}
}

// argoRolloutWithRef is a spec.workloadRef Rollout: it carries NO inline pod
// template, borrowing its pod definition from the referenced workload.
func argoRolloutWithRef(name, ns string, replicas int64, refKind, refName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Rollout",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{
			"replicas": float64(replicas),
			"workloadRef": map[string]any{
				"apiVersion": "apps/v1",
				"kind":       refKind,
				"name":       refName,
			},
		},
	}}
}

// argoRolloutInlineNoReplicas is an inline-template Rollout with no spec.replicas
// field, which Argo defaults to 1 (an active workload).
func argoRolloutInlineNoReplicas(name, ns string, tmplLabels map[string]string) *unstructured.Unstructured {
	lbls := map[string]any{}
	for k, v := range tmplLabels {
		lbls[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Rollout",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{
			"template": map[string]any{"metadata": map[string]any{"labels": lbls}},
		},
	}}
}

// argoRolloutRawRef is a Rollout carrying a spec.workloadRef built from raw
// fields (e.g. an empty or partially-populated reference) and no inline template.
func argoRolloutRawRef(name, ns string, replicas int64, ref map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Rollout",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{
			"replicas":    float64(replicas),
			"workloadRef": ref,
		},
	}}
}

// initDormancyEnv installs the global typed cache plus, when rollouts are given,
// the dynamic Rollout cache (synced), so scaledToZeroBackingWorkload can be
// called directly.
func initDormancyEnv(t *testing.T, typed []runtime.Object, rolloutObjs ...*unstructured.Unstructured) {
	t.Helper()
	if err := InitTestResourceCache(fake.NewClientset(typed...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	if len(rolloutObjs) == 0 {
		return
	}
	rolloutGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	objs := make([]runtime.Object, 0, len(rolloutObjs))
	for _, r := range rolloutObjs {
		objs = append(objs, r)
	}
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{rolloutGVR: "RolloutList"},
		objs...,
	)
	if err := InitTestDynamicResourceCache(dynClient, []APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout", Name: "rollouts", Namespaced: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	dynCache := GetDynamicResourceCache()
	if err := dynCache.EnsureWatching(rolloutGVR); err != nil {
		t.Fatalf("EnsureWatching rollouts: %v", err)
	}
	if !dynCache.WaitForSync(rolloutGVR, 2*time.Second) {
		t.Fatal("rollout dynamic cache did not sync")
	}
}

func lbl() map[string]string { return map[string]string{"app": "api"} }

// TestDormancy_ActiveSiblingVetoesDormant: a Service matching one workload parked
// at 0 and one active workload must NOT read as intentionally scaled to zero - the
// active sibling disproves dormancy. Covers same-kind, cross-kind, and reversed
// declaration order (an active workload wins regardless of scan order).
func TestDormancy_ActiveSiblingVetoesDormant(t *testing.T) {
	cases := []struct {
		name  string
		typed []runtime.Object
	}{
		{"two deployments, dormant first", []runtime.Object{
			deploymentWithReplicas("api-old", "prod", 0, lbl()),
			deploymentWithReplicas("api-new", "prod", 2, lbl()),
		}},
		{"two deployments, active first", []runtime.Object{
			deploymentWithReplicas("api-new", "prod", 2, lbl()),
			deploymentWithReplicas("api-old", "prod", 0, lbl()),
		}},
		{"deployment dormant + statefulset active", []runtime.Object{
			deploymentWithReplicas("api-old", "prod", 0, lbl()),
			statefulSetWithReplicas("api-sts", "prod", 1, lbl()),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer ResetTestState()
			defer ResetTestDynamicState()
			initDormancyEnv(t, tc.typed)
			zero, outcome := scaledToZeroBackingWorkload(GetResourceCache(), scaleZeroSvc("prod"))
			if zero {
				t.Error("an active matching sibling must prevent a scaled-to-zero (benign) verdict")
			}
			if outcome != rolloutLookupConclusive {
				t.Errorf("active veto outcome = %v, want conclusive", outcome)
			}
		})
	}
}

// TestDormancy_AllParkedIsDormant: when every matching workload is parked at 0,
// the Service is genuinely intentionally off (benign).
func TestDormancy_AllParkedIsDormant(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initDormancyEnv(t, []runtime.Object{
		deploymentWithReplicas("api-a", "prod", 0, lbl()),
		statefulSetWithReplicas("api-b", "prod", 0, lbl()),
	})
	zero, outcome := scaledToZeroBackingWorkload(GetResourceCache(), scaleZeroSvc("prod"))
	if !zero {
		t.Error("all matching workloads parked at 0 must read as scaled-to-zero (benign)")
	}
	if outcome != rolloutLookupConclusive {
		t.Errorf("all-parked outcome = %v, want conclusive", outcome)
	}
}

// TestDormancy_WorkloadRefRolloutActiveVetoes is the core workloadRef case: a Rollout
// borrows its pod template from a Deployment that Argo parks at 0, while the
// Rollout itself wants pods. The parked referenced Deployment must NOT make the
// Service benign - resolving the workloadRef reveals the Rollout is active. This
// fails without workloadRef resolution (the Rollout has no inline template, so it
// would be invisible and the parked Deployment would win).
func TestDormancy_WorkloadRefRolloutActiveVetoes(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initDormancyEnv(t,
		[]runtime.Object{deploymentWithReplicas("api-tmpl", "prod", 0, lbl())},
		argoRolloutWithRef("api-rollout", "prod", 3, "Deployment", "api-tmpl"),
	)
	zero, outcome := scaledToZeroBackingWorkload(GetResourceCache(), scaleZeroSvc("prod"))
	if zero {
		t.Error("an active workloadRef Rollout (resolved via its referenced Deployment) must veto dormancy")
	}
	if outcome != rolloutLookupConclusive {
		t.Errorf("workloadRef active veto outcome = %v, want conclusive", outcome)
	}
}

// TestDormancy_WorkloadRefRolloutParkedIsDormant: a workloadRef Rollout genuinely
// scaled to 0, borrowing from a Deployment also at 0, is benign. The selector is
// matched through the referenced Deployment's template labels.
func TestDormancy_WorkloadRefRolloutParkedIsDormant(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	// The referenced Deployment carries the matching pod-template labels (as a real
	// workloadRef setup does). This guards that resolving the ref and reading the
	// Rollout's own replicas=0 keeps the benign verdict rather than flipping it to
	// active or ref-unresolved.
	initDormancyEnv(t,
		[]runtime.Object{deploymentWithReplicas("api-tmpl", "prod", 0, lbl())},
		argoRolloutWithRef("api-rollout", "prod", 0, "Deployment", "api-tmpl"),
	)
	zero, outcome := scaledToZeroBackingWorkload(GetResourceCache(), scaleZeroSvc("prod"))
	if !zero {
		t.Error("a workloadRef Rollout at 0 (and its referenced Deployment at 0) must read as benign dormancy")
	}
	if outcome != rolloutLookupConclusive {
		t.Errorf("workloadRef dormant outcome = %v, want conclusive", outcome)
	}
}

// TestDormancy_UnreadableRefIsUnknown: a matching Rollout whose workloadRef can't
// be resolved - a missing referenced Deployment, or an unsupported ref kind - must
// read as "can't confirm" (ref-unresolved), never a confident benign/outage.
func TestDormancy_UnreadableRefIsUnknown(t *testing.T) {
	cases := []struct {
		name    string
		rollout *unstructured.Unstructured
	}{
		{"referenced deployment missing", argoRolloutWithRef("api-rollout", "prod", 0, "Deployment", "does-not-exist")},
		{"unsupported ref kind", argoRolloutWithRef("api-rollout", "prod", 0, "CloneSet", "api-clone")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer ResetTestState()
			defer ResetTestDynamicState()
			initDormancyEnv(t, nil, tc.rollout)
			zero, outcome := scaledToZeroBackingWorkload(GetResourceCache(), scaleZeroSvc("prod"))
			if zero {
				t.Error("an unresolvable ref must not produce a confident scaled-to-zero verdict")
			}
			if outcome != rolloutLookupRefUnresolved {
				t.Errorf("unresolvable ref outcome = %v, want ref-unresolved", outcome)
			}
		})
	}
}

// TestDormancy_EmptyOrPartialRefIsUnknown: a Rollout that carries a spec.workloadRef
// object but doesn't fully populate it (empty, or apiVersion only) must read as
// "can't confirm", NOT fall through to the absent inline template and let a parked
// typed sibling make the Service benign.
func TestDormancy_EmptyOrPartialRefIsUnknown(t *testing.T) {
	cases := []struct {
		name string
		ref  map[string]any
	}{
		{"empty ref object", map[string]any{}},
		{"apiVersion only, no kind/name", map[string]any{"apiVersion": "apps/v1"}},
		{"kind without name", map[string]any{"apiVersion": "apps/v1", "kind": "Deployment"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer ResetTestState()
			defer ResetTestDynamicState()
			// A parked Deployment sibling would otherwise be read as benign; the
			// unresolved ref must keep the Service non-downgraded.
			initDormancyEnv(t,
				[]runtime.Object{deploymentWithReplicas("api-old", "prod", 0, lbl())},
				argoRolloutRawRef("api-rollout", "prod", 0, tc.ref),
			)
			zero, outcome := scaledToZeroBackingWorkload(GetResourceCache(), scaleZeroSvc("prod"))
			if zero {
				t.Error("an unresolved workloadRef must not let a parked sibling make the Service benign")
			}
			if outcome != rolloutLookupRefUnresolved {
				t.Errorf("empty/partial ref outcome = %v, want ref-unresolved", outcome)
			}
		})
	}
}

// TestNoReadyEndpoints_UnresolvableRefStaysCritical: a genuine zero-ready-pods
// outage must stay critical (with a caveat) when the only uncertainty is an
// unresolvable Rollout workloadRef. An unrelated broken reference in the namespace
// must not downgrade the Service's outage to a warning.
func TestNoReadyEndpoints_UnresolvableRefStaysCritical(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	old := metav1.NewTime(time.Now().Add(-30 * time.Minute))
	initDormancyEnv(t,
		[]runtime.Object{
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "api-0", Namespace: "prod", Labels: lbl(), CreationTimestamp: old},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}},
			},
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", CreationTimestamp: old},
				Spec:       corev1.ServiceSpec{Selector: lbl(), Ports: []corev1.ServicePort{{Port: 80}}},
			},
		},
		// An unrelated Rollout with an unsupported (unresolvable) workloadRef.
		argoRolloutWithRef("unrelated-rollout", "prod", 0, "CloneSet", "whatever"),
	)
	var got *Detection
	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(GetResourceCache(), "prod")
		for i := range problems {
			p := &problems[i]
			if p.Kind == "Service" && p.Name == "api" && p.Fingerprint == NoReadyEndpointsFingerprint {
				got = p
				break
			}
		}
		if got != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got == nil {
		t.Fatalf("missing Service/api no-ready-endpoints row; got: %+v", problems)
	}
	if got.Severity != "critical" {
		t.Errorf("an unresolvable Rollout ref must NOT downgrade a real outage; severity = %q, want critical", got.Severity)
	}
	if !strings.Contains(got.Message, "couldn't resolve") {
		t.Errorf("expected a caveat about the unresolvable Rollout, got message %q", got.Message)
	}
}

// TestDormancy_RolloutOnlyParkedIsDormant: a benign verdict resting only on the
// Rollout cache (no typed workload at all) is still reached when the Rollout scan
// is complete - the typed/Rollout dormancy split must not break the plain case.
func TestDormancy_RolloutOnlyParkedIsDormant(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initDormancyEnv(t, nil, argoRolloutScaled("api-rollout", "prod", 0, lbl()))
	zero, outcome := scaledToZeroBackingWorkload(GetResourceCache(), scaleZeroSvc("prod"))
	if !zero {
		t.Error("a single parked Rollout (synced) must read as benign dormancy")
	}
	if outcome != rolloutLookupConclusive {
		t.Errorf("rollout-only dormant outcome = %v, want conclusive", outcome)
	}
}

// TestDormancy_ActiveWinsOverUnknown: active evidence disproves dormancy even when
// another matching Rollout is unresolvable - the unknown must not short-circuit the
// scan before the active workload is seen (order-independent precedence).
func TestDormancy_ActiveWinsOverUnknown(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initDormancyEnv(t,
		[]runtime.Object{deploymentWithReplicas("api-active", "prod", 2, lbl())},
		argoRolloutWithRef("api-rollout", "prod", 0, "Deployment", "does-not-exist"),
	)
	zero, outcome := scaledToZeroBackingWorkload(GetResourceCache(), scaleZeroSvc("prod"))
	if zero {
		t.Error("an active workload must veto dormancy regardless of an unresolvable sibling")
	}
	if outcome != rolloutLookupConclusive {
		t.Errorf("active-over-unknown outcome = %v, want conclusive", outcome)
	}
}

// TestDormancy_RolloutNoReplicasIsActive: a matching Rollout with no spec.replicas
// defaults to 1 (active) and must veto a parked sibling's dormancy.
func TestDormancy_RolloutNoReplicasIsActive(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initDormancyEnv(t,
		[]runtime.Object{deploymentWithReplicas("api-old", "prod", 0, lbl())},
		argoRolloutInlineNoReplicas("api-rollout", "prod", lbl()),
	)
	zero, outcome := scaledToZeroBackingWorkload(GetResourceCache(), scaleZeroSvc("prod"))
	if zero {
		t.Error("a Rollout with no replicas field (Argo default 1) is active and must veto dormancy")
	}
	if outcome != rolloutLookupConclusive {
		t.Errorf("default-replicas active outcome = %v, want conclusive", outcome)
	}
}

// TestDormancy_NilReplicasDeploymentIsActive: a matching Deployment with a nil
// Replicas pointer defaults to 1 (active) and must veto a parked sibling.
func TestDormancy_NilReplicasDeploymentIsActive(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	nilReplicaDep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api-default", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: lbl()}}},
	}
	initDormancyEnv(t, []runtime.Object{
		nilReplicaDep,
		statefulSetWithReplicas("api-sts", "prod", 0, lbl()),
	})
	zero, outcome := scaledToZeroBackingWorkload(GetResourceCache(), scaleZeroSvc("prod"))
	if zero {
		t.Error("a Deployment with nil replicas (default 1) is active and must veto dormancy")
	}
	if outcome != rolloutLookupConclusive {
		t.Errorf("nil-replicas active outcome = %v, want conclusive", outcome)
	}
}
