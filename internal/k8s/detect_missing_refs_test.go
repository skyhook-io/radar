package k8s

import (
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

// TestDetectMissingRefs covers each dangling-ref check exactly once
// against a single fixture. Each assertion pins one production-impact
// path (pod-won't-schedule, route-returns-nothing, binding-grants-no-permissions, etc.).
// Refs to RESOURCES THAT EXIST in the fixture are confirmed NOT flagged —
// the boolean asymmetry "we know it's missing vs we can't tell" is
// load-bearing for false-positive avoidance.
func TestDetectMissingRefs(t *testing.T) {
	defer ResetTestState()

	now := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	optTrue := true
	scName := "fast"
	scMissing := "does-not-exist"

	// Resources that DO exist — referencing these must NOT flag.
	existingCM := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "real-cm", Namespace: "prod", CreationTimestamp: now}}
	existingSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "real-secret", Namespace: "prod", CreationTimestamp: now}}
	existingPVC := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "real-pvc", Namespace: "prod", CreationTimestamp: now}}
	existingSA := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "real-sa", Namespace: "prod", CreationTimestamp: now}}
	existingSvc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "real-svc", Namespace: "prod", CreationTimestamp: now}}
	existingSC := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fast", CreationTimestamp: now}, Provisioner: "test"}
	existingRole := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "real-role", Namespace: "prod", CreationTimestamp: now}}
	existingClusterRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "real-cr", CreationTimestamp: now}}
	existingDep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "real-deploy", Namespace: "prod", CreationTimestamp: now}}

	// Pod with multiple missing refs (one of each type).
	podMissing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "broken", Namespace: "prod", CreationTimestamp: now},
		Spec: corev1.PodSpec{
			ServiceAccountName: "missing-sa",
			Volumes: []corev1.Volume{
				{Name: "pv", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "missing-pvc"}}},
				{Name: "cm-vol", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "missing-cm-vol"}}}},
				{Name: "sec-vol", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "missing-sec-vol"}}},
				// Optional CM ref MUST NOT be flagged.
				{Name: "cm-opt", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "optional-cm-missing"}, Optional: &optTrue}}},
			},
			ImagePullSecrets: []corev1.LocalObjectReference{
				{Name: "missing-pull"},
				{Name: "real-secret"}, // existing — must NOT be flagged
			},
			Containers: []corev1.Container{{
				Name: "app",
				EnvFrom: []corev1.EnvFromSource{
					{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "missing-cm-envfrom"}}},
					{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "missing-sec-envfrom"}}},
				},
			}},
		},
	}

	// Pod referencing only existing things — must produce zero rows.
	podHealthy := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "prod", CreationTimestamp: now},
		Spec: corev1.PodSpec{
			ServiceAccountName: "real-sa",
			Volumes: []corev1.Volume{
				{Name: "pv", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "real-pvc"}}},
				{Name: "cm-vol", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "real-cm"}}}},
			},
			Containers: []corev1.Container{{
				Name: "app",
				EnvFrom: []corev1.EnvFromSource{
					{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "real-secret"}}},
				},
			}},
		},
	}

	// Pod using default SA — must NOT be flagged (default is auto-created).
	podDefaultSA := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "default-sa", Namespace: "prod", CreationTimestamp: now},
		Spec:       corev1.PodSpec{ServiceAccountName: "default"},
	}

	// HPAs
	hpaMissingTarget := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "hpa-bad", Namespace: "prod", CreationTimestamp: now},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "missing-dep"},
			MaxReplicas:    3,
		},
	}
	hpaExistingTarget := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "hpa-ok", Namespace: "prod", CreationTimestamp: now},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "real-deploy"},
			MaxReplicas:    3,
		},
	}
	// Unknown target Kind (e.g., a custom scalable CRD): must NOT be flagged.
	hpaUnknownKind := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "hpa-crd", Namespace: "prod", CreationTimestamp: now},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Cluster", Name: "anything"},
			MaxReplicas:    3,
		},
	}

	// Service with an http port, used for port-match testing.
	existingSvc.Spec = corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 80}}}

	// Ingress: missing backend Service + missing port + missing TLS secret.
	// Mixed with valid refs so the negative-assert path also runs.
	ingMixed := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "ing", Namespace: "prod", CreationTimestamp: now},
		Spec: networkingv1.IngressSpec{
			DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "real-svc", Port: networkingv1.ServiceBackendPort{Name: "http"}}},
			Rules: []networkingv1.IngressRule{{
				Host: "app.example.com",
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{
						// Missing Service.
						{Path: "/api", Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "missing-svc", Port: networkingv1.ServiceBackendPort{Number: 8080}}}},
						// Existing Service, wrong port name.
						{Path: "/bad-port", Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "real-svc", Port: networkingv1.ServiceBackendPort{Name: "grpc-not-there"}}}},
					},
				}},
			}},
			TLS: []networkingv1.IngressTLS{
				{Hosts: []string{"app.example.com"}, SecretName: "missing-tls"},
				{Hosts: []string{"other.example.com"}, SecretName: "real-secret"}, // existing — must NOT be flagged
			},
		},
	}
	ingMissingDefault := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "ing-default", Namespace: "prod", CreationTimestamp: now},
		Spec: networkingv1.IngressSpec{
			DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "missing-default-svc", Port: networkingv1.ServiceBackendPort{Number: 80}}},
		},
	}

	// StatefulSet with missing headless service.
	stsMissingSvc := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "sts-bad", Namespace: "prod", CreationTimestamp: now},
		Spec:       appsv1.StatefulSetSpec{ServiceName: "missing-headless"},
	}
	stsExistingSvc := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "sts-ok", Namespace: "prod", CreationTimestamp: now},
		Spec:       appsv1.StatefulSetSpec{ServiceName: "real-svc"},
	}

	// PVCs
	pvcExplicitMissingSC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-bad-sc", Namespace: "prod", CreationTimestamp: now},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: &scMissing},
	}
	pvcExistingSC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-ok-sc", Namespace: "prod", CreationTimestamp: now},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: &scName},
	}
	pvcDefaultSC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-default", Namespace: "prod", CreationTimestamp: now},
		// StorageClassName=nil → cluster default; must NOT be flagged.
	}

	// RBAC bindings
	rbMissing := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "rb-bad", Namespace: "prod", CreationTimestamp: now},
		RoleRef:    rbacv1.RoleRef{Kind: "Role", Name: "missing-role"},
	}
	rbExisting := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "rb-ok", Namespace: "prod", CreationTimestamp: now},
		RoleRef:    rbacv1.RoleRef{Kind: "Role", Name: "real-role"},
	}
	crbMissing := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "crb-bad", CreationTimestamp: now},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "missing-cr"},
	}
	crbExisting := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "crb-ok", CreationTimestamp: now},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "real-cr"},
	}

	client := fake.NewClientset(
		existingCM, existingSecret, existingPVC, existingSA, existingSvc,
		existingSC, existingRole, existingClusterRole, existingDep,
		podMissing, podHealthy, podDefaultSA,
		hpaMissingTarget, hpaExistingTarget, hpaUnknownKind,
		ingMixed, ingMissingDefault,
		stsMissingSvc, stsExistingSvc,
		pvcExplicitMissingSC, pvcExistingSC, pvcDefaultSC,
		rbMissing, rbExisting, crbMissing, crbExisting,
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	// Wait for informers to populate before asserting. Need at least 12
	// distinct {kind,reason} hits — 8 from the original 8-check set plus
	// 4 from the new ones (imagePullSecret, headless Service, TLS Secret,
	// backend port match).
	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectMissingRefs(cache, "")
		if len(problems) >= 12 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	type want struct {
		kind, ns, name, reason string
	}
	mustHave := []want{
		{"Pod", "prod", "broken", "Missing PVC"},
		{"Pod", "prod", "broken", "Missing ServiceAccount"},
		{"Pod", "prod", "broken", "Missing ConfigMap"},
		{"Pod", "prod", "broken", "Missing Secret"},
		{"Pod", "prod", "broken", "Missing imagePullSecret"},
		{"StatefulSet", "prod", "sts-bad", "Missing headless Service"},
		{"HorizontalPodAutoscaler", "prod", "hpa-bad", "Missing scaleTargetRef"},
		{"Ingress", "prod", "ing", "Missing backend Service"},
		{"Ingress", "prod", "ing-default", "Missing backend Service"},
		{"Ingress", "prod", "ing", "Missing backend Service port"},
		{"Ingress", "prod", "ing", "Missing TLS Secret"},
		{"PersistentVolumeClaim", "prod", "pvc-bad-sc", "Missing StorageClass"},
		{"RoleBinding", "prod", "rb-bad", "Missing roleRef target"},
		{"ClusterRoleBinding", "", "crb-bad", "Missing roleRef target"},
	}

	for _, w := range mustHave {
		if !findProblem(problems, w.kind, w.ns, w.name, w.reason) {
			t.Errorf("missing expected problem: %+v\ngot: %+v", w, problems)
		}
	}

	// Negative assertions — referencing existing resources must NOT flag.
	type forbid struct {
		kind, name, reasonPrefix string
	}
	forbidden := []forbid{
		{"Pod", "ok", "Missing"},
		{"Pod", "default-sa", "Missing ServiceAccount"},
		{"HorizontalPodAutoscaler", "hpa-ok", "Missing"},
		{"HorizontalPodAutoscaler", "hpa-crd", "Missing"}, // unknown kind → not verifiable → not flagged
		{"PersistentVolumeClaim", "pvc-ok-sc", "Missing"},
		{"PersistentVolumeClaim", "pvc-default", "Missing"}, // nil storageClassName uses default
		{"RoleBinding", "rb-ok", "Missing"},
		{"ClusterRoleBinding", "crb-ok", "Missing"},
		{"StatefulSet", "sts-ok", "Missing"},
	}
	for _, f := range forbidden {
		for _, p := range problems {
			if p.Kind == f.kind && p.Name == f.name && hasPrefix(p.Reason, f.reasonPrefix) {
				t.Errorf("unexpected problem flagged: %+v (forbidden=%+v)", p, f)
			}
		}
	}

	// Optional-flag MUST suppress the CM ref in volumes.
	for _, p := range problems {
		if p.Kind == "Pod" && p.Name == "broken" && p.Reason == "Missing ConfigMap" {
			if hasSubstr(p.Message, "optional-cm-missing") {
				t.Errorf("optional CM ref must NOT be flagged, got: %+v", p)
			}
		}
	}

	// ClusterRoleBinding rows must NOT appear when narrowing by namespace.
	nsScoped := DetectMissingRefs(cache, "prod")
	for _, p := range nsScoped {
		if p.Kind == "ClusterRoleBinding" {
			t.Errorf("ClusterRoleBinding leaked into namespace-scoped result: %+v", p)
		}
	}

	// Severity is calibrated to impact, not blanket-critical. Refs that break a
	// running thing now stay critical; latent/inert ones are de-escalated:
	//   - Missing TLS Secret → warning (controller falls back to default cert)
	//   - Missing headless Service on a single-replica STS → info (no peers, inert)
	//   - Missing roleRef target → warning (dangling binding grants nothing)
	for _, p := range problems {
		var wantSev string
		switch p.Reason {
		case "Missing TLS Secret":
			wantSev = "warning"
		case "Missing headless Service":
			wantSev = "info" // sts-bad has nil replicas → treated as 1
		case "Missing roleRef target":
			wantSev = "warning"
		default:
			wantSev = "critical"
		}
		if p.Severity != wantSev {
			t.Errorf("reason %q: severity = %q, want %q: %+v", p.Reason, p.Severity, wantSev, p)
		}
	}

	// Every dangling-ref finding must carry a structured cause + next-step action
	// (the operator-facing fix). Pin it so a new ref detector can't regress to a
	// bare message with no remediation.
	for _, p := range problems {
		if !hasPrefix(p.Reason, "Missing") {
			continue
		}
		if p.Cause == "" {
			t.Errorf("missing-ref %q (%s/%s) has empty Cause", p.Reason, p.Kind, p.Name)
		}
		if p.Action == "" {
			t.Errorf("missing-ref %q (%s/%s) has empty Action", p.Reason, p.Kind, p.Name)
		}
	}

	assertProblemActionOrder(t, problems, "Pod", "prod", "broken", "Missing PVC", "missing-pvc", "existing PVC", "create PVC")
	assertProblemActionContains(t, problems, "Pod", "prod", "broken", "Missing PVC", "pod template")
	assertProblemActionContains(t, problems, "Pod", "prod", "broken", "Missing PVC", "recreate this Pod")
	assertProblemActionOrder(t, problems, "Pod", "prod", "broken", "Missing ServiceAccount", "missing-sa", "existing ServiceAccount", "create ServiceAccount")
	assertProblemActionContains(t, problems, "Pod", "prod", "broken", "Missing ServiceAccount", "pod template")
	assertProblemActionOrder(t, problems, "Pod", "prod", "broken", "Missing imagePullSecret", "missing-pull", "existing pull Secret", "create pull Secret")
	assertProblemActionContains(t, problems, "Pod", "prod", "broken", "Missing imagePullSecret", "pod template")
	assertProblemActionOrder(t, problems, "HorizontalPodAutoscaler", "prod", "hpa-bad", "Missing scaleTargetRef", "missing-dep", "existing workload", "create Deployment")
	assertProblemActionOrder(t, problems, "Ingress", "prod", "ing", "Missing backend Service", "missing-svc", "existing Service", "create Service")
	assertProblemActionOrder(t, problems, "Ingress", "prod", "ing", "Missing backend Service port", "grpc-not-there", "already exposes", "add port")
	assertProblemActionOrder(t, problems, "Ingress", "prod", "ing-default", "Missing backend Service", "missing-default-svc", "existing Service", "create Service")
	assertProblemActionNotContains(t, problems, "Ingress", "prod", "ing-default", "Missing backend Service", "rule")
	assertProblemActionNotContains(t, problems, "Ingress", "prod", "ing-default", "Missing backend Service", "route")
	assertProblemActionOrder(t, problems, "Ingress", "prod", "ing", "Missing TLS Secret", "missing-tls", "existing kubernetes.io/tls Secret", "create TLS Secret")
	assertProblemActionStarts(t, problems, "StatefulSet", "prod", "sts-bad", "Missing headless Service", "Create headless Service")
	assertProblemActionContains(t, problems, "StatefulSet", "prod", "sts-bad", "Missing headless Service", "immutable")
	assertProblemActionOrder(t, problems, "PersistentVolumeClaim", "prod", "pvc-bad-sc", "Missing StorageClass", "does-not-exist", "existing StorageClass", "create StorageClass")
	assertProblemActionContains(t, problems, "PersistentVolumeClaim", "prod", "pvc-bad-sc", "Missing StorageClass", "immutable")
	assertProblemActionOrder(t, problems, "RoleBinding", "prod", "rb-bad", "Missing roleRef target", "missing-role", "existing role", "create Role")
	assertProblemActionContains(t, problems, "RoleBinding", "prod", "rb-bad", "Missing roleRef target", "immutable")
	assertProblemActionOrder(t, problems, "ClusterRoleBinding", "", "crb-bad", "Missing roleRef target", "missing-cr", "existing role", "create ClusterRole")
	assertProblemActionContains(t, problems, "ClusterRoleBinding", "", "crb-bad", "Missing roleRef target", "immutable")
}

func TestScaleTargetLookupResultDistinguishesErrors(t *testing.T) {
	gr := schema.GroupResource{Group: "apps", Resource: "deployments"}

	checked, exists := rolloutServiceLookupResult("prod", "checkout", "missing", apierrors.NewNotFound(schema.GroupResource{Resource: "services"}, "missing"))
	if !checked || exists {
		t.Fatalf("rollout service not found result = (%v, %v), want checked missing", checked, exists)
	}

	checked, exists = rolloutServiceLookupResult("prod", "checkout", "blocked", apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "blocked", errors.New("denied")))
	if checked || exists {
		t.Fatalf("rollout service forbidden result = (%v, %v), want unchecked", checked, exists)
	}

	checked, exists = scaleTargetLookupResult("Deployment", "prod", "missing", apierrors.NewNotFound(gr, "missing"))
	if !checked || exists {
		t.Fatalf("not found result = (%v, %v), want checked missing", checked, exists)
	}

	checked, exists = scaleTargetLookupResult("Deployment", "prod", "blocked", apierrors.NewForbidden(gr, "blocked", errors.New("denied")))
	if checked || exists {
		t.Fatalf("forbidden result = (%v, %v), want unchecked", checked, exists)
	}
}

func TestDetectPodMissingRefs_SkipsTerminalPods(t *testing.T) {
	defer ResetTestState()
	now := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	mkPod := func(name string, phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", CreationTimestamp: now},
			Spec:       corev1.PodSpec{ServiceAccountName: "missing-sa"},
			Status:     corev1.PodStatus{Phase: phase},
		}
	}
	client := fake.NewClientset(
		mkPod("live", corev1.PodRunning),
		mkPod("done", corev1.PodSucceeded),
		mkPod("failed", corev1.PodFailed),
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	deadline := time.Now().Add(2 * time.Second)
	flagged := map[string]bool{}
	for time.Now().Before(deadline) {
		flagged = map[string]bool{}
		for _, p := range DetectMissingRefs(cache, "") {
			if p.Reason == "Missing ServiceAccount" {
				flagged[p.Name] = true
			}
		}
		if flagged["live"] {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !flagged["live"] {
		t.Error("running pod with a missing ServiceAccount should be flagged")
	}
	if flagged["done"] || flagged["failed"] {
		t.Errorf("terminal pods (Succeeded/Failed) must be skipped by missing-ref detection: %+v", flagged)
	}
}

func TestDetectPodMissingRefs_OwnerGrouped(t *testing.T) {
	defer ResetTestState()
	now := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	tru := true
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "web-abc", Namespace: "prod", CreationTimestamp: now,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Controller: &tru}},
	}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web-abc-1", Namespace: "prod", CreationTimestamp: now,
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-abc", Controller: &tru}},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:    "c",
			EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "nope"}}}},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	client := fake.NewClientset(rs, pod)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	deadline := time.Now().Add(2 * time.Second)
	var got *Detection
	for time.Now().Before(deadline) {
		got = nil
		for _, p := range DetectMissingRefs(cache, "") {
			if p.Kind == "Pod" && p.Reason == "Missing ConfigMap" {
				pp := p
				got = &pp
			}
		}
		if got != nil && got.OwnerKind != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got == nil {
		t.Fatal("expected a Missing ConfigMap pod problem")
	}
	if got.OwnerGroup != "apps" || got.OwnerKind != "Deployment" || got.OwnerName != "web" {
		t.Errorf("owner = %s/%s/%s, want apps/Deployment/web (pod missing-refs must fold under the workload)", got.OwnerGroup, got.OwnerKind, got.OwnerName)
	}
}

func TestTopOwnerForPodResolved(t *testing.T) {
	defer ResetTestState()
	tru := true
	depRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "web-abc123", Namespace: "ns",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Controller: &tru}},
	}}
	rolloutRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "canary-xyz789", Namespace: "ns",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "Rollout", Name: "canary", Controller: &tru}},
	}}
	client := fake.NewClientset(depRS, rolloutRS)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	mkPod := func(rs string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Namespace:       "ns",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs, Controller: &tru}},
		}}
	}
	// Wait for the ReplicaSet informer to populate (resolver returns the real
	// Deployment once cached; before that it falls back to the hash-strip guess).
	deadline := time.Now().Add(2 * time.Second)
	var dep *TopOwnerInfo
	for time.Now().Before(deadline) {
		dep = topOwnerForPodResolved(cache, mkPod("web-abc123"))
		if dep != nil && dep.Name == "web" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if dep == nil || dep.Kind != "Deployment" || dep.Name != "web" {
		t.Errorf("Deployment-owned pod resolved to %+v, want Deployment/web", dep)
	}
	ro := topOwnerForPodResolved(cache, mkPod("canary-xyz789"))
	if ro == nil || ro.Kind != "Rollout" || ro.Name != "canary" || ro.Group != "argoproj.io" {
		t.Errorf("Rollout-owned pod resolved to %+v, want argoproj.io/Rollout/canary (NOT a phantom Deployment)", ro)
	}
}

func TestDanglingRoleBindingSeverity(t *testing.T) {
	cases := []struct {
		name, binding, roleRef, want string
	}{
		{"ordinary dangling binding is warning", "my-app-binding", "missing-role", "warning"},
		{"GKE PSP residue by binding name is info", "gce:podsecuritypolicy:privileged", "gce:podsecuritypolicy:privileged", "info"},
		{"GKE PSP residue by roleRef name is info", "some-binding", "gce:podsecuritypolicy:unprivileged", "info"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := danglingRoleBindingSeverity(c.binding, c.roleRef); got != c.want {
				t.Errorf("danglingRoleBindingSeverity(%q,%q) = %q, want %q", c.binding, c.roleRef, got, c.want)
			}
		})
	}
}

// TestStatefulSetHeadlessServiceSeverity pins the replica-aware calibration:
// a missing headless Service is inert (info) for a single-replica StatefulSet
// but a real peer-DNS degradation (warning) for a multi-replica one.
func TestStatefulSetHeadlessServiceSeverity(t *testing.T) {
	defer ResetTestState()
	now := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	three := int32(3)
	single := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "sts-single", Namespace: "prod", CreationTimestamp: now},
		Spec:       appsv1.StatefulSetSpec{ServiceName: "missing-headless"},
	}
	multi := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "sts-multi", Namespace: "prod", CreationTimestamp: now},
		Spec:       appsv1.StatefulSetSpec{ServiceName: "missing-headless", Replicas: &three},
	}
	client := fake.NewClientset(single, multi)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	deadline := time.Now().Add(2 * time.Second)
	var got map[string]string
	for time.Now().Before(deadline) {
		got = map[string]string{}
		for _, p := range DetectMissingRefs(cache, "") {
			if p.Kind == "StatefulSet" && p.Reason == "Missing headless Service" {
				got[p.Name] = p.Severity
			}
		}
		if len(got) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got["sts-single"] != "info" {
		t.Errorf("single-replica STS severity = %q, want info", got["sts-single"])
	}
	if got["sts-multi"] != "warning" {
		t.Errorf("multi-replica STS severity = %q, want warning", got["sts-multi"])
	}
}

func TestDetectMissingWebhookRefs(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()

	now := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	existingSvc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "webhook-ok", Namespace: "hooks", CreationTimestamp: now}}
	client := fake.NewClientset(existingSvc)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	vwhGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	mwhGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{
			vwhGVR: "ValidatingWebhookConfigurationList",
			mwhGVR: "MutatingWebhookConfigurationList",
		},
		webhookConfig("ValidatingWebhookConfiguration", "validate-hooks", now, []any{
			webhookWithService("missing", "hooks", "does-not-exist"),
			webhookWithService("existing", "hooks", "webhook-ok"),
			webhookWithURL("external"),
		}),
		webhookConfig("MutatingWebhookConfiguration", "mutate-hooks", now, []any{
			webhookWithService("missing-mutating", "hooks", "mutating-missing"),
		}),
	)
	if err := InitTestDynamicResourceCache(dynClient, []APIResource{
		{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingWebhookConfiguration", Name: "validatingwebhookconfigurations", Verbs: []string{"list", "watch"}},
		{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "MutatingWebhookConfiguration", Name: "mutatingwebhookconfigurations", Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}

	dynCache := GetDynamicResourceCache()
	discovery := GetResourceDiscovery()
	if err := dynCache.EnsureWatching(vwhGVR); err != nil {
		t.Fatalf("EnsureWatching validating webhooks: %v", err)
	}
	if err := dynCache.EnsureWatching(mwhGVR); err != nil {
		t.Fatalf("EnsureWatching mutating webhooks: %v", err)
	}
	if !dynCache.WaitForSync(vwhGVR, 2*time.Second) {
		t.Fatal("validating webhook dynamic cache did not sync")
	}
	if !dynCache.WaitForSync(mwhGVR, 2*time.Second) {
		t.Fatal("mutating webhook dynamic cache did not sync")
	}

	problems := DetectMissingWebhookRefs(GetResourceCache(), dynCache, discovery, "")
	if !findProblem(problems, "ValidatingWebhookConfiguration", "", "validate-hooks", "Missing webhook backend Service") {
		t.Fatalf("missing validating webhook Service not detected: %+v", problems)
	}
	if !findProblem(problems, "MutatingWebhookConfiguration", "", "mutate-hooks", "Missing webhook backend Service") {
		t.Fatalf("missing mutating webhook Service not detected: %+v", problems)
	}
	if len(problems) != 2 {
		t.Fatalf("expected exactly 2 missing webhook refs, got %+v", problems)
	}
	for _, p := range problems {
		if p.Namespace != "" {
			t.Errorf("webhook configs are cluster-scoped; got namespace on problem: %+v", p)
		}
		if hasSubstr(p.Message, "webhook-ok") || hasSubstr(p.Message, "external") {
			t.Errorf("existing Service or URL-based webhook should not flag: %+v", p)
		}
	}
	if scoped := DetectMissingWebhookRefs(GetResourceCache(), dynCache, discovery, "hooks"); len(scoped) != 0 {
		t.Fatalf("namespace-scoped call should omit cluster-scoped webhook configs, got %+v", scoped)
	}
}

func TestDetectMissingGatewayRefs(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()

	now := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	existingSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", CreationTimestamp: now},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 80}}},
	}
	crossNsSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "platform", CreationTimestamp: now},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	grantedCrossNsSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-granted", Namespace: "platform", CreationTimestamp: now},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	client := fake.NewClientset(existingSvc, crossNsSvc, grantedCrossNsSvc)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	routeGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	refGrantGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1beta1", Resource: "referencegrants"}
	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{
			routeGVR:    "HTTPRouteList",
			refGrantGVR: "ReferenceGrantList",
		},
		gatewayRoute("broken", "prod", now, []any{
			map[string]any{"name": "missing", "port": int64(80)},
			map[string]any{"name": "api", "port": int64(9090)},
			map[string]any{"name": "api", "port": int64(80)},
			map[string]any{"name": "api"},
			map[string]any{"name": "shared", "namespace": "platform", "port": int64(8080)},
			map[string]any{"name": "shared-granted", "namespace": "platform", "port": int64(8080)},
			map[string]any{"group": "storage.k8s.io", "kind": "StorageClass", "name": "not-service"},
		}),
		gatewayReferenceGrant("allow-shared-granted", "platform", now, "", "HTTPRoute", "prod", "shared-granted"),
	)
	if err := InitTestDynamicResourceCache(dynClient, []APIResource{
		{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute", Name: "httproutes", Verbs: []string{"list", "watch"}},
		{Group: "gateway.networking.k8s.io", Version: "v1beta1", Kind: "ReferenceGrant", Name: "referencegrants", Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	dynCache := GetDynamicResourceCache()
	if err := dynCache.EnsureWatching(routeGVR); err != nil {
		t.Fatalf("EnsureWatching httproutes: %v", err)
	}
	if !dynCache.WaitForSync(routeGVR, 2*time.Second) {
		t.Fatal("httproute dynamic cache did not sync")
	}
	if err := dynCache.EnsureWatching(refGrantGVR); err != nil {
		t.Fatalf("EnsureWatching referencegrants: %v", err)
	}
	if !dynCache.WaitForSync(refGrantGVR, 2*time.Second) {
		t.Fatal("referencegrant dynamic cache did not sync")
	}

	problems := DetectMissingGatewayRefs(GetResourceCache(), dynCache, GetResourceDiscovery(), "")
	if !findProblem(problems, "HTTPRoute", "prod", "broken", "Missing Gateway backend Service") {
		t.Fatalf("missing Gateway backend Service not detected: %+v", problems)
	}
	if !findProblem(problems, "HTTPRoute", "prod", "broken", "Missing Gateway backend Service port") {
		t.Fatalf("missing Gateway backend Service port not detected: %+v", problems)
	}
	if !findProblem(problems, "HTTPRoute", "prod", "broken", "Missing Gateway ReferenceGrant") {
		t.Fatalf("missing Gateway ReferenceGrant not detected: %+v", problems)
	}
	if len(problems) != 4 {
		t.Fatalf("expected exactly 4 Gateway missing-ref problems, got %+v", problems)
	}
	assertProblemActionOrder(t, problems, "HTTPRoute", "prod", "broken", "Missing Gateway backend Service", "missing", "existing Service", "create Service")
	assertProblemActionOrder(t, problems, "HTTPRoute", "prod", "broken", "Missing Gateway backend Service port", "9090", "already exposes", "add port")
	assertProblemActionStarts(t, problems, "HTTPRoute", "prod", "broken", "Missing Gateway ReferenceGrant", "Create a ReferenceGrant")

	scoped := DetectMissingGatewayRefs(GetResourceCache(), dynCache, GetResourceDiscovery(), "prod")
	if len(scoped) != 4 {
		t.Fatalf("namespace-scoped Gateway refs should include prod route problems, got %+v", scoped)
	}
}

func TestDetectMissingCRDRefs(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()

	now := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	replicas := int32(1)
	client := fake.NewClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "stable", Namespace: "prod", CreationTimestamp: now}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod", CreationTimestamp: now},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		},
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	rolloutGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	scaledObjectGVR := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{
			rolloutGVR:      "RolloutList",
			scaledObjectGVR: "ScaledObjectList",
		},
		argoRollout("checkout", "prod", now, map[string]any{
			"canary": map[string]any{
				"stableService": "stable",
				"canaryService": "missing-canary",
			},
		}),
		argoRollout("preview", "prod", now, map[string]any{
			"blueGreen": map[string]any{
				"previewService": "missing-preview",
			},
		}),
		kedaScaledObject("ok", "prod", now, "apps/v1", "Deployment", "web"),
		kedaScaledObject("missing-target", "prod", now, "apps/v1", "Deployment", "ghost"),
		kedaScaledObject("missing-default-deployment-target", "prod", now, "apps/v1", "", "also-ghost"),
		kedaScaledObject("wrong-group-target", "prod", now, "example.com/v1", "Deployment", "ghost"),
		kedaScaledObject("unsupported-target", "prod", now, "example.com/v1", "Widget", "ghost"),
	)
	if err := InitTestDynamicResourceCache(dynClient, []APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout", Name: "rollouts", Verbs: []string{"list", "watch"}},
		{Group: "keda.sh", Version: "v1alpha1", Kind: "ScaledObject", Name: "scaledobjects", Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	dynCache := GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{rolloutGVR, scaledObjectGVR} {
		if err := dynCache.EnsureWatching(gvr); err != nil {
			t.Fatalf("EnsureWatching %s: %v", gvr.String(), err)
		}
		if !dynCache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("%s dynamic cache did not sync", gvr.String())
		}
	}

	problems := DetectMissingCRDRefs(GetResourceCache(), dynCache, GetResourceDiscovery(), "")
	if !findProblem(problems, "Rollout", "prod", "checkout", "Missing Rollout Service") {
		t.Fatalf("missing Rollout canary Service not detected: %+v", problems)
	}
	if !findProblem(problems, "Rollout", "prod", "preview", "Missing Rollout Service") {
		t.Fatalf("missing Rollout preview Service not detected: %+v", problems)
	}
	if !findProblem(problems, "ScaledObject", "prod", "missing-target", "Missing scaleTargetRef") {
		t.Fatalf("missing KEDA scaleTargetRef not detected: %+v", problems)
	}
	if !findProblem(problems, "ScaledObject", "prod", "missing-default-deployment-target", "Missing scaleTargetRef") {
		t.Fatalf("KEDA scaleTargetRef with omitted kind should default to Deployment: %+v", problems)
	}
	if len(problems) != 4 {
		t.Fatalf("expected exactly 4 curated CRD missing refs, got %+v", problems)
	}
	assertProblemActionOrder(t, problems, "Rollout", "prod", "checkout", "Missing Rollout Service", "missing-canary", "existing Service", "create Service")
	assertProblemActionContains(t, problems, "Rollout", "prod", "checkout", "Missing Rollout Service", "spec.strategy.canary.canaryService")
	assertProblemActionNotContains(t, problems, "Rollout", "prod", "checkout", "Missing Rollout Service", "traffic-routing")
	assertProblemActionOrder(t, problems, "ScaledObject", "prod", "missing-target", "Missing scaleTargetRef", "ghost", "existing workload", "create Deployment")
	for _, p := range problems {
		if p.Severity != "warning" {
			t.Errorf("curated CRD missing refs should be warning-level, got %+v", p)
		}
		if hasSubstr(p.Message, "unsupported-target") || hasSubstr(p.Message, "wrong-group-target") || hasSubstr(p.Message, "stable") || hasSubstr(p.Message, "web") {
			t.Errorf("existing or unsupported refs should not flag: %+v", p)
		}
	}

	scoped := DetectMissingCRDRefs(GetResourceCache(), dynCache, GetResourceDiscovery(), "prod")
	if len(scoped) != 4 {
		t.Fatalf("namespace-scoped CRD refs should include prod problems, got %+v", scoped)
	}
}

func webhookConfig(kind, name string, ts metav1.Time, webhooks []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admissionregistration.k8s.io/v1",
		"kind":       kind,
		"metadata": map[string]any{
			"name":              name,
			"creationTimestamp": ts.Format(time.RFC3339),
		},
		"webhooks": webhooks,
	}}
}

func gatewayRoute(name, namespace string, ts metav1.Time, backendRefs []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]any{
			"name":              name,
			"namespace":         namespace,
			"creationTimestamp": ts.Format(time.RFC3339),
		},
		"spec": map[string]any{
			"rules": []any{
				map[string]any{"backendRefs": backendRefs},
			},
		},
	}}
}

func argoRollout(name, namespace string, ts metav1.Time, strategy map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Rollout",
		"metadata": map[string]any{
			"name":              name,
			"namespace":         namespace,
			"creationTimestamp": ts.Format(time.RFC3339),
		},
		"spec": map[string]any{
			"strategy": strategy,
		},
	}}
}

func kedaScaledObject(name, namespace string, ts metav1.Time, apiVersion, kind, targetName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "keda.sh/v1alpha1",
		"kind":       "ScaledObject",
		"metadata": map[string]any{
			"name":              name,
			"namespace":         namespace,
			"creationTimestamp": ts.Format(time.RFC3339),
		},
		"spec": map[string]any{
			"scaleTargetRef": map[string]any{
				"apiVersion": apiVersion,
				"kind":       kind,
				"name":       targetName,
			},
		},
	}}
}

func gatewayReferenceGrant(name, namespace string, ts metav1.Time, fromGroup, fromKind, fromNamespace, toService string) *unstructured.Unstructured {
	from := map[string]any{"kind": fromKind, "namespace": fromNamespace}
	if fromGroup != "" {
		from["group"] = fromGroup
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1beta1",
		"kind":       "ReferenceGrant",
		"metadata": map[string]any{
			"name":              name,
			"namespace":         namespace,
			"creationTimestamp": ts.Format(time.RFC3339),
		},
		"spec": map[string]any{
			"from": []any{
				from,
			},
			"to": []any{
				map[string]any{"group": "", "kind": "Service", "name": toService},
			},
		},
	}}
}

func webhookWithService(name, namespace, service string) map[string]any {
	return map[string]any{
		"name": name,
		"clientConfig": map[string]any{
			"service": map[string]any{
				"name":      service,
				"namespace": namespace,
			},
		},
	}
}

func webhookWithURL(name string) map[string]any {
	return map[string]any{
		"name": name,
		"clientConfig": map[string]any{
			"url": "https://example.com/webhook",
		},
	}
}

// --- helpers ---

func findProblem(ps []Detection, kind, ns, name, reason string) bool {
	for _, p := range ps {
		if p.Kind == kind && p.Namespace == ns && p.Name == name && p.Reason == reason {
			return true
		}
	}
	return false
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func hasSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func assertProblemActionOrder(t *testing.T, ps []Detection, kind, ns, name, reason, actionSubstr, earlier, later string) {
	t.Helper()
	for _, p := range ps {
		if p.Kind != kind || p.Namespace != ns || p.Name != name || p.Reason != reason {
			continue
		}
		if actionSubstr != "" && !strings.Contains(p.Action, actionSubstr) {
			continue
		}
		if strings.HasPrefix(p.Action, "Create ") {
			t.Fatalf("action should not lead with create: %q", p.Action)
		}
		assertSubstringOrder(t, p.Action, earlier, later)
		return
	}
	t.Fatalf("missing problem action for %s %s/%s reason %q action containing %q; got %+v", kind, ns, name, reason, actionSubstr, ps)
}

func assertProblemActionStarts(t *testing.T, ps []Detection, kind, ns, name, reason, prefix string) {
	t.Helper()
	for _, p := range ps {
		if p.Kind == kind && p.Namespace == ns && p.Name == name && p.Reason == reason {
			if !strings.HasPrefix(p.Action, prefix) {
				t.Fatalf("action for %s %s/%s reason %q = %q, want prefix %q", kind, ns, name, reason, p.Action, prefix)
			}
			return
		}
	}
	t.Fatalf("missing problem action for %s %s/%s reason %q; got %+v", kind, ns, name, reason, ps)
}

func assertProblemActionContains(t *testing.T, ps []Detection, kind, ns, name, reason, substr string) {
	t.Helper()
	for _, p := range ps {
		if p.Kind == kind && p.Namespace == ns && p.Name == name && p.Reason == reason {
			if !strings.Contains(p.Action, substr) {
				t.Fatalf("action for %s %s/%s reason %q = %q, want substring %q", kind, ns, name, reason, p.Action, substr)
			}
			return
		}
	}
	t.Fatalf("missing problem action for %s %s/%s reason %q; got %+v", kind, ns, name, reason, ps)
}

func assertProblemActionNotContains(t *testing.T, ps []Detection, kind, ns, name, reason, substr string) {
	t.Helper()
	for _, p := range ps {
		if p.Kind == kind && p.Namespace == ns && p.Name == name && p.Reason == reason {
			if strings.Contains(p.Action, substr) {
				t.Fatalf("action for %s %s/%s reason %q = %q, should not contain %q", kind, ns, name, reason, p.Action, substr)
			}
			return
		}
	}
	t.Fatalf("missing problem action for %s %s/%s reason %q; got %+v", kind, ns, name, reason, ps)
}

func assertSubstringOrder(t *testing.T, text, earlier, later string) {
	t.Helper()
	first := strings.Index(text, earlier)
	second := strings.Index(text, later)
	if first < 0 || second < 0 {
		t.Fatalf("action %q must contain %q and %q", text, earlier, later)
	}
	if first > second {
		t.Fatalf("action should mention %q before %q: %q", earlier, later, text)
	}
}

func TestRefDiagHelpers(t *testing.T) {
	r, m, c, a := cmRefDiag("volume", "app-config", "prod")
	if r != "Missing ConfigMap" {
		t.Errorf("reason = %q, want Missing ConfigMap", r)
	}
	if !hasSubstr(m, "app-config") || !hasSubstr(m, "volume") {
		t.Errorf("message should name the site + ConfigMap: %q", m)
	}
	if !hasSubstr(c, "app-config") {
		t.Errorf("cause should name the ConfigMap: %q", c)
	}
	if !hasSubstr(a, "app-config") || !hasSubstr(a, "prod") {
		t.Errorf("action should name target + namespace: %q", a)
	}
	if !hasSubstr(a, "pod template") || !hasSubstr(a, "recreate this Pod") {
		t.Errorf("action should mention pod-template/recreate framing: %q", a)
	}
	assertSubstringOrder(t, a, "existing ConfigMap", "create ConfigMap")

	r2, _, _, a2 := secretRefDiag("envFrom", "db-creds", "prod")
	if r2 != "Missing Secret" {
		t.Errorf("reason = %q, want Missing Secret", r2)
	}
	if !hasSubstr(a2, "db-creds") || !hasSubstr(a2, "prod") {
		t.Errorf("secret action should name target + namespace: %q", a2)
	}
	if !hasSubstr(a2, "pod template") || !hasSubstr(a2, "recreate this Pod") {
		t.Errorf("secret action should mention pod-template/recreate framing: %q", a2)
	}
	assertSubstringOrder(t, a2, "existing Secret", "create Secret")
}
