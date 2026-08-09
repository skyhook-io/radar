package k8s

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
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

func TestMissingRefProblemUsesInjectedClockForResourceAge(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	createdAt := now.Add(-90 * time.Minute)
	got := missingRefProblem(now, "Pod", "", "shop", "web", "Missing Secret", "missing", createdAt)
	if got.AgeSeconds != int64((90*time.Minute).Seconds()) || got.Age != "1h" {
		t.Fatalf("resource age = %q/%ds, want injected-clock age 1h/5400s", got.Age, got.AgeSeconds)
	}
}

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

func TestDetectMissingEnvKeysClassifiesCurrentImpact(t *testing.T) {
	defer ResetTestState()
	created := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	optional := true
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "prod"},
		Data:       map[string]string{"present": "value"},
		BinaryData: map[string][]byte{"binary": []byte("value")},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "prod"},
		Data:       map[string][]byte{"present": []byte("value")},
	}
	env := func(configKey, secretKey string, optionalRef bool) []corev1.EnvVar {
		var optionalPtr *bool
		if optionalRef {
			optionalPtr = &optional
		}
		return []corev1.EnvVar{
			{Name: "CONFIG", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}, Key: configKey, Optional: optionalPtr}}},
			{Name: "SECRET", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "credentials"}, Key: secretKey, Optional: optionalPtr}}},
		}
	}
	pod := func(name string, vars []corev1.EnvVar, status corev1.ContainerStatus) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", CreationTimestamp: created},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: vars}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{status}},
		}
	}
	runningStatus := corev1.ContainerStatus{Name: "app", ContainerID: "containerd://running", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	waitingStatus := corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError"}}}
	runningRisk := pod("running-risk", env("missing", "missing", false), runningStatus)
	blockedNow := pod("blocked-now", env("missing", "present", false), waitingStatus)
	blockedNow.Status.Phase = corev1.PodPending
	optionalMissing := pod("optional-missing", env("missing", "missing", true), runningStatus)
	binaryOnly := pod("binary-only", env("binary", "present", false), waitingStatus)
	binaryOnly.Status.Phase = corev1.PodPending
	crashLoop := pod("crash-loop", env("missing", "present", false), corev1.ContainerStatus{
		Name:                 "app",
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
	})
	healthy := pod("healthy", env("present", "present", false), runningStatus)
	runningSourceRisk := pod("running-source-risk", nil, runningStatus)
	runningSourceRisk.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "missing-source"}}}}
	mixedImpact := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mixed-impact", Namespace: "prod", CreationTimestamp: created},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "running", Env: env("missing", "present", false)[:1]},
			{Name: "blocked", Env: env("missing", "present", false)[:1]},
		}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "running", ContainerID: "containerd://running", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			{Name: "blocked", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError"}}},
		}},
	}

	client := fake.NewClientset(configMap, secret, runningRisk, blockedNow, optionalMissing, binaryOnly, crashLoop, healthy, runningSourceRisk, mixedImpact)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	deadline := time.Now().Add(2 * time.Second)
	var detections []Detection
	for time.Now().Before(deadline) {
		detections = DetectMissingRefs(cache, "prod")
		if len(detections) >= 5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	want := map[string]string{
		"running-risk/Missing ConfigMap key": "warning",
		"running-risk/Missing Secret key":    "warning",
		"blocked-now/Missing ConfigMap key":  "critical",
		"binary-only/Missing ConfigMap key":  "critical",
		"crash-loop/Missing ConfigMap key":   "critical",
		"running-source-risk/Missing Secret": "warning",
		"mixed-impact/Missing ConfigMap key": "critical",
	}
	for _, detection := range detections {
		key := detection.Name + "/" + detection.Reason
		severity, expected := want[key]
		if !expected {
			if detection.Name == "optional-missing" || detection.Name == "healthy" {
				t.Errorf("unexpected missing environment detection: %+v", detection)
			}
			continue
		}
		if detection.Severity != severity {
			t.Errorf("%s severity = %q, want %q: %+v", key, detection.Severity, severity, detection)
		}
		if detection.Cause == "" || detection.Action == "" {
			t.Errorf("%s lacks cause/action: %+v", key, detection)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing environment detections: %+v; got %+v", want, detections)
	}
}

// TestScaleTargetLookupResultDistinguishesErrors pins the coverage-gated
// tri-state for the two sibling CRD-ref lookups (Argo Rollout Service refs, KEDA
// scaleTargetRef workloads). Both must mirror refLookupResult: a NotFound is an
// authoritative "missing" ONLY when the target informer covers the namespace; an
// uncovered miss or any non-NotFound error is "couldn't verify" and must not
// emit a false warning on a namespace-restricted install.
func TestScaleTargetLookupResultDistinguishesErrors(t *testing.T) {
	// deployments/services cluster-wide → every namespace is covered.
	coveredCache := scopedRefTestCache(t, fake.NewClientset(), map[string]k8score.ResourceScope{
		"services":    {Enabled: true},
		"deployments": {Enabled: true},
	})
	// deployments/services scoped to "prod" → "staging" misses are unverifiable.
	prodScopedCache := scopedRefTestCache(t, fake.NewClientset(), map[string]k8score.ResourceScope{
		"services":    {Enabled: true, Namespace: "prod"},
		"deployments": {Enabled: true, Namespace: "prod"},
	})

	svcGR := schema.GroupResource{Resource: "services"}
	svcNotFound := apierrors.NewNotFound(svcGR, "missing")
	svcForbidden := apierrors.NewForbidden(svcGR, "blocked", errors.New("denied"))
	depGR := schema.GroupResource{Group: "apps", Resource: "deployments"}
	depNotFound := apierrors.NewNotFound(depGR, "missing")
	depForbidden := apierrors.NewForbidden(depGR, "blocked", errors.New("denied"))

	// --- rolloutServiceLookupResult (Argo Rollout → Service) ---
	if checked, exists := rolloutServiceLookupResult(coveredCache, "prod", "checkout", "web", nil); !checked || !exists {
		t.Fatalf("covered present service = (%v, %v), want checked exists", checked, exists)
	}
	if checked, exists := rolloutServiceLookupResult(coveredCache, "prod", "checkout", "missing", svcNotFound); !checked || exists {
		t.Fatalf("covered missing service = (%v, %v), want checked missing", checked, exists)
	}
	if checked, exists := rolloutServiceLookupResult(prodScopedCache, "staging", "checkout", "missing", svcNotFound); checked || exists {
		t.Fatalf("uncovered service miss = (%v, %v), want unverifiable (no false missing)", checked, exists)
	}
	if checked, exists := rolloutServiceLookupResult(coveredCache, "prod", "checkout", "blocked", svcForbidden); checked || exists {
		t.Fatalf("forbidden service = (%v, %v), want unverifiable", checked, exists)
	}

	// --- scaleTargetLookupResult (KEDA scaleTargetRef → workload) ---
	if checked, exists := scaleTargetLookupResult(coveredCache, "deployments", "Deployment", "prod", "web", nil); !checked || !exists {
		t.Fatalf("covered present workload = (%v, %v), want checked exists", checked, exists)
	}
	if checked, exists := scaleTargetLookupResult(coveredCache, "deployments", "Deployment", "prod", "missing", depNotFound); !checked || exists {
		t.Fatalf("covered missing workload = (%v, %v), want checked missing", checked, exists)
	}
	if checked, exists := scaleTargetLookupResult(prodScopedCache, "deployments", "Deployment", "staging", "missing", depNotFound); checked || exists {
		t.Fatalf("uncovered workload miss = (%v, %v), want unverifiable (no false missing)", checked, exists)
	}
	if checked, exists := scaleTargetLookupResult(coveredCache, "deployments", "Deployment", "prod", "blocked", depForbidden); checked || exists {
		t.Fatalf("forbidden workload = (%v, %v), want unverifiable", checked, exists)
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
	if !findProblem(problems, "ValidatingWebhookConfiguration", "", "validate-hooks", MissingWebhookBackendReason) {
		t.Fatalf("missing validating webhook Service not detected: %+v", problems)
	}
	if !findProblem(problems, "MutatingWebhookConfiguration", "", "mutate-hooks", MissingWebhookBackendReason) {
		t.Fatalf("missing mutating webhook Service not detected: %+v", problems)
	}
	if len(problems) != 2 {
		t.Fatalf("expected exactly 2 missing webhook refs, got %+v", problems)
	}
	foundValidatingMissing := false
	for _, p := range problems {
		if p.Namespace != "" {
			t.Errorf("webhook configs are cluster-scoped; got namespace on problem: %+v", p)
		}
		if hasSubstr(p.Message, "webhook-ok") || hasSubstr(p.Message, "external") {
			t.Errorf("existing Service or URL-based webhook should not flag: %+v", p)
		}
		if !p.OnsetUnknown || p.Duration != "" || p.DurationSeconds != 0 {
			t.Errorf("missing webhook Service must not use configuration age as outage onset: %+v", p)
		}
		if p.Kind == "ValidatingWebhookConfiguration" && p.Name == "validate-hooks" {
			foundValidatingMissing = true
			if p.Fingerprint != WebhookBackendFingerprint("hooks", "does-not-exist") {
				t.Errorf("missing webhook Service fingerprint = %q, want exact backend identity", p.Fingerprint)
			}
		}
	}
	if !foundValidatingMissing {
		t.Fatal("validating webhook missing-Service row not found for fingerprint assertion")
	}
	refs := AdmissionWebhookServiceReferences(dynCache, discovery)
	if len(refs) != 3 {
		t.Fatalf("service-backed webhook refs = %+v, want three and no URL-backed ref", refs)
	}
	watched := AdmissionWebhookServiceReferencesWatched(dynCache, discovery)
	if len(watched) != len(refs) {
		t.Fatalf("watched service-backed webhook refs = %+v, want %+v", watched, refs)
	}
	for _, problem := range problems {
		matched := false
		for _, ref := range watched {
			if ref.ConfigurationKind == problem.Kind && ref.ConfigurationName == problem.Name &&
				WebhookBackendFingerprint(ref.ServiceNamespace, ref.ServiceName) == problem.Fingerprint {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("missing webhook row fingerprint has no matching watched inventory ref: problem=%+v refs=%+v", problem, watched)
		}
	}
	if refs[0].FailurePolicy != "Fail" {
		t.Fatalf("omitted failurePolicy must normalize to Fail: %+v", refs[0])
	}
	if scoped := DetectMissingWebhookRefs(GetResourceCache(), dynCache, discovery, "hooks"); len(scoped) != 0 {
		t.Fatalf("namespace-scoped call should omit cluster-scoped webhook configs, got %+v", scoped)
	}
}

func TestAdmissionWebhookServiceReferencesWatchedDoesNotStartInformer(t *testing.T) {
	defer ResetTestDynamicState()

	vwhGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{vwhGVR: "ValidatingWebhookConfigurationList"},
		webhookConfig("ValidatingWebhookConfiguration", "validate-hooks", metav1.Now(), []any{
			webhookWithService("validate", "hooks", "policy-webhook"),
		}),
	)
	if err := InitTestDynamicResourceCache(dynClient, []APIResource{{
		Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingWebhookConfiguration", Name: "validatingwebhookconfigurations", Verbs: []string{"list", "watch"},
	}}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}

	dynCache := GetDynamicResourceCache()
	if refs := AdmissionWebhookServiceReferencesWatched(dynCache, GetResourceDiscovery()); len(refs) != 0 {
		t.Fatalf("unwatched webhook refs = %+v, want none", refs)
	}
	if watched := dynCache.GetWatchedResources(); len(watched) != 0 {
		t.Fatalf("request-time webhook correlation started informers: %+v", watched)
	}
}

func TestDetectIngressMissingIngressClass(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	old := metav1.NewTime(now.Add(-10 * time.Minute))
	young := metav1.NewTime(now.Add(-time.Minute))
	missing := "missing"
	existing := "nginx"
	objects := []runtime.Object{
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: existing}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "prod", CreationTimestamp: old}, Spec: networkingv1.IngressSpec{IngressClassName: &missing}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: "prod", CreationTimestamp: old}, Spec: networkingv1.IngressSpec{IngressClassName: &existing}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "young", Namespace: "prod", CreationTimestamp: young}, Spec: networkingv1.IngressSpec{IngressClassName: &missing}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "addressed", Namespace: "prod", CreationTimestamp: old}, Spec: networkingv1.IngressSpec{IngressClassName: &missing}, Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{{Hostname: "example.test"}}}}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "prod", CreationTimestamp: old, Annotations: map[string]string{"kubernetes.io/ingress.class": "nginx"}}, Spec: networkingv1.IngressSpec{IngressClassName: &missing}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "cloud", Namespace: "prod", CreationTimestamp: old, Annotations: map[string]string{"alb.ingress.kubernetes.io/scheme": "internet-facing"}}, Spec: networkingv1.IngressSpec{IngressClassName: &missing}},
	}
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	problems := detectIngressMissingBackend(GetResourceCache(), "prod", now)
	if len(problems) != 1 {
		t.Fatalf("IngressClass problems = %+v, want only the old authoritative named miss", problems)
	}
	problem := problems[0]
	if problem.Name != "missing" || problem.Reason != "Missing IngressClass" || problem.Severity != "warning" {
		t.Fatalf("unexpected missing IngressClass problem: %+v", problem)
	}
	if !problem.OnsetUnknown || problem.Duration != "" || problem.DurationSeconds != 0 || problem.AgeSeconds <= 0 {
		t.Fatalf("missing IngressClass must preserve resource age without inventing onset: %+v", problem)
	}
}

func TestDetectMissingWebhookRefsFailurePolicySeverity(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()

	now := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	client := fake.NewClientset()
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	vwhGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{vwhGVR: "ValidatingWebhookConfigurationList"},
		webhookConfig("ValidatingWebhookConfiguration", "validate-policy", now, []any{
			webhookWithServicePolicy("explicit-ignore", "hooks", "ignore-svc", "Ignore"),
			webhookWithServicePolicy("explicit-fail", "hooks", "fail-svc", "Fail"),
			webhookWithService("default-fail", "hooks", "default-svc"),
			webhookWithServicePolicy("mixed-ignore", "hooks", "mixed-svc", "Ignore"),
			webhookWithServicePolicy("mixed-fail", "hooks", "mixed-svc", "Fail"),
			webhookWithURL("external"),
		}),
	)
	if err := InitTestDynamicResourceCache(dynClient, []APIResource{
		{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingWebhookConfiguration", Name: "validatingwebhookconfigurations", Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	dynCache := GetDynamicResourceCache()
	if err := dynCache.EnsureWatching(vwhGVR); err != nil {
		t.Fatalf("EnsureWatching validating webhooks: %v", err)
	}
	if !dynCache.WaitForSync(vwhGVR, 2*time.Second) {
		t.Fatal("validating webhook dynamic cache did not sync")
	}

	problems := DetectMissingWebhookRefs(GetResourceCache(), dynCache, GetResourceDiscovery(), "")
	if len(problems) != 4 {
		t.Fatalf("expected one row per missing backend Service, got %d: %+v", len(problems), problems)
	}
	assertWebhookBackendSeverity(t, problems, "ignore-svc", "warning", "failurePolicy=Ignore")
	assertWebhookBackendSeverity(t, problems, "fail-svc", "critical", "failurePolicy=Fail")
	assertWebhookBackendSeverity(t, problems, "default-svc", "critical", "failurePolicy=Fail")
	assertWebhookBackendSeverity(t, problems, "mixed-svc", "critical", "failurePolicy=Fail/Ignore")
	assertWebhookBackendMessage(t, problems, "mixed-svc", `webhooks "mixed-fail", "mixed-ignore" clientConfig.service reference Service`)
	for _, p := range problems {
		if hasSubstr(p.Message, "external") {
			t.Errorf("URL-based webhook should not flag: %+v", p)
		}
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

	var portMismatch *Detection
	for i := range problems {
		if problems[i].Reason == "Missing Gateway backend Service port" && hasSubstr(problems[i].Message, "9090") {
			portMismatch = &problems[i]
			break
		}
	}
	if portMismatch == nil {
		t.Fatalf("port-mismatch problem for port 9090 not found: %+v", problems)
	}

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
	return webhookWithServicePolicy(name, namespace, service, "")
}

func webhookWithServicePolicy(name, namespace, service, failurePolicy string) map[string]any {
	webhook := map[string]any{
		"name": name,
		"clientConfig": map[string]any{
			"service": map[string]any{
				"name":      service,
				"namespace": namespace,
			},
		},
	}
	if failurePolicy != "" {
		webhook["failurePolicy"] = failurePolicy
	}
	return webhook
}

func assertWebhookBackendSeverity(t *testing.T, problems []Detection, service, severity, policyText string) {
	t.Helper()
	for _, p := range problems {
		if !hasSubstr(p.Message, service) {
			continue
		}
		if p.Severity != severity {
			t.Fatalf("%s severity = %q, want %q: %+v", service, p.Severity, severity, p)
		}
		if !hasSubstr(p.Message, policyText) || !hasSubstr(p.Cause, "failurePolicy=") {
			t.Fatalf("%s should name policy in message/cause, got message=%q cause=%q", service, p.Message, p.Cause)
		}
		return
	}
	t.Fatalf("missing webhook backend problem for Service %s: %+v", service, problems)
}

func assertWebhookBackendMessage(t *testing.T, problems []Detection, service, text string) {
	t.Helper()
	for _, p := range problems {
		if hasSubstr(p.Message, service) {
			if !hasSubstr(p.Message, text) {
				t.Fatalf("%s message = %q, want substring %q", service, p.Message, text)
			}
			return
		}
	}
	t.Fatalf("missing webhook backend problem for Service %s: %+v", service, problems)
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

// TestRefLookupResult pins the tri-state classification driving every
// missing-ref emit: a lister miss is authoritative ONLY when the informer
// covers the target namespace AND the error is a genuine NotFound. Anything
// else is "couldn't verify" and must fail toward silence.
func TestRefLookupResult(t *testing.T) {
	coveredCache := scopedRefTestCache(t, fake.NewClientset(),
		map[string]k8score.ResourceScope{"services": {Enabled: true}})
	scopedCache := scopedRefTestCache(t, fake.NewClientset(),
		map[string]k8score.ResourceScope{"services": {Enabled: true, Namespace: "prod"}})

	notFound := apierrors.NewNotFound(schema.GroupResource{Resource: "services"}, "web")
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "web", errors.New("denied"))

	cases := []struct {
		name           string
		cache          *ResourceCache
		ns             string
		err            error
		wantVerifiable bool
		wantExists     bool
	}{
		{"nil error is exists regardless of scope", scopedCache, "other", nil, true, true},
		{"covered NotFound is authoritative missing", coveredCache, "prod", notFound, true, false},
		{"scoped NotFound in own namespace is authoritative", scopedCache, "prod", notFound, true, false},
		{"scoped NotFound in OTHER namespace is unverifiable", scopedCache, "staging", notFound, false, false},
		{"non-NotFound error is unverifiable even when covered", coveredCache, "prod", forbidden, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verifiable, exists := refLookupResult(tc.cache, "services", tc.ns, tc.err)
			if verifiable != tc.wantVerifiable || exists != tc.wantExists {
				t.Errorf("refLookupResult = (%v, %v), want (%v, %v)", verifiable, exists, tc.wantVerifiable, tc.wantExists)
			}
			knownMissing := refKnownMissing(tc.cache, "services", tc.ns, tc.err)
			if knownMissing != (tc.wantVerifiable && !tc.wantExists) {
				t.Errorf("refKnownMissing = %v, inconsistent with lookup (%v, %v)", knownMissing, tc.wantVerifiable, tc.wantExists)
			}
		})
	}
}

// scopedRefTestCache builds a standalone ResourceCache (not the singleton)
// with explicit per-kind scopes, so tests can model a namespace-restricted
// install where target informers don't watch every namespace.
func scopedRefTestCache(t *testing.T, client *fake.Clientset, scopes map[string]k8score.ResourceScope) *ResourceCache {
	t.Helper()
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:         client,
		ResourceScopes: scopes,
		DeferredTypes:  map[string]bool{},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	return &ResourceCache{ResourceCache: core}
}

// TestDetectMissingRefsRespectsCacheCoverage models a namespace-restricted
// install: target informers (services, configmaps, secrets, serviceaccounts,
// persistentvolumeclaims, deployments, roles) are scoped to "prod" while
// source informers are cluster-wide. Sources in "staging" whose targets the
// cache cannot observe must produce NO findings — a per-namespace lister
// answers NotFound for every namespace it doesn't watch, indistinguishable
// from true absence. Genuine misses in the covered namespace must keep
// flagging critical.
func TestDetectMissingRefsRespectsCacheCoverage(t *testing.T) {
	now := metav1.NewTime(time.Now().Add(-5 * time.Minute))

	// Targets that exist in staging — invisible to prod-scoped informers.
	// Flagging any of them "missing" would be a confident lie.
	stagingSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "staging", CreationTimestamp: now},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	stagingCM := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "staging", CreationTimestamp: now}}
	stagingDep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "staging", CreationTimestamp: now}}

	// Staging sources: every target ref is either present-but-unobservable or
	// absent-but-unverifiable. All must stay silent.
	stagingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "stg-pod", Namespace: "staging", CreationTimestamp: now},
		Spec: corev1.PodSpec{
			ServiceAccountName: "stg-sa-missing",
			Volumes: []corev1.Volume{
				{Name: "cm", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}}},
				{Name: "sec", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "stg-secret-missing"}}},
				{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "stg-pvc-missing"}}},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	stagingIng := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "stg-ing", Namespace: "staging", CreationTimestamp: now},
		Spec: networkingv1.IngressSpec{
			DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "web", Port: networkingv1.ServiceBackendPort{Number: 80}}},
			TLS:            []networkingv1.IngressTLS{{SecretName: "stg-tls-missing"}},
		},
	}
	stagingSTS := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "stg-sts", Namespace: "staging", CreationTimestamp: now},
		Spec:       appsv1.StatefulSetSpec{ServiceName: "web"},
	}
	stagingHPA := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "stg-hpa", Namespace: "staging", CreationTimestamp: now},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"},
			MaxReplicas:    3,
		},
	}
	stagingRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "stg-rb", Namespace: "staging", CreationTimestamp: now},
		RoleRef:    rbacv1.RoleRef{Kind: "Role", Name: "stg-role-missing"},
	}

	// Prod sources with genuinely-missing targets: coverage holds, so these
	// must keep flagging. They double as the informer-sync sentinel.
	prodIng := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-ing", Namespace: "prod", CreationTimestamp: now},
		Spec: networkingv1.IngressSpec{
			DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "really-missing", Port: networkingv1.ServiceBackendPort{Number: 80}}},
		},
	}
	prodPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-pod", Namespace: "prod", CreationTimestamp: now},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{
				{Name: "cm", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "really-missing-cm"}}}},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	// Prod source whose target exists in prod — covered+present, no finding.
	prodSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", CreationTimestamp: now},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	prodIngOK := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-ing-ok", Namespace: "prod", CreationTimestamp: now},
		Spec: networkingv1.IngressSpec{
			DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "api", Port: networkingv1.ServiceBackendPort{Number: 80}}},
		},
	}

	client := fake.NewClientset(
		stagingSvc, stagingCM, stagingDep,
		stagingPod, stagingIng, stagingSTS, stagingHPA, stagingRB,
		prodIng, prodPod, prodSvc, prodIngOK,
	)
	cw := k8score.ResourceScope{Enabled: true}
	prodScoped := k8score.ResourceScope{Enabled: true, Namespace: "prod"}
	cache := scopedRefTestCache(t, client, map[string]k8score.ResourceScope{
		// Sources: cluster-wide so staging objects are scanned at all.
		"pods":                     cw,
		"ingresses":                cw,
		"statefulsets":             cw,
		"horizontalpodautoscalers": cw,
		"rolebindings":             cw,
		// Targets: namespace-scoped to prod — staging misses are unverifiable.
		"services":               prodScoped,
		"configmaps":             prodScoped,
		"secrets":                prodScoped,
		"serviceaccounts":        prodScoped,
		"persistentvolumeclaims": prodScoped,
		"deployments":            prodScoped,
		"roles":                  prodScoped,
	})

	// Wait until (a) the covered-namespace sentinels flag and (b) the staging
	// sources are visibly listed — otherwise the absence assertions below
	// could pass vacuously against an unsynced cache.
	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectMissingRefs(cache, "")
		stagingSourcesListed := func() bool {
			if _, err := cache.Ingresses().Ingresses("staging").Get("stg-ing"); err != nil {
				return false
			}
			if _, err := cache.Pods().Pods("staging").Get("stg-pod"); err != nil {
				return false
			}
			if _, err := cache.StatefulSets().StatefulSets("staging").Get("stg-sts"); err != nil {
				return false
			}
			if _, err := cache.HorizontalPodAutoscalers().HorizontalPodAutoscalers("staging").Get("stg-hpa"); err != nil {
				return false
			}
			if _, err := cache.RoleBindings().RoleBindings("staging").Get("stg-rb"); err != nil {
				return false
			}
			if _, err := cache.Services().Services("prod").Get("api"); err != nil {
				return false
			}
			return true
		}
		if findProblem(problems, "Ingress", "prod", "prod-ing", "Missing backend Service") &&
			findProblem(problems, "Pod", "prod", "prod-pod", "Missing ConfigMap") &&
			stagingSourcesListed() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Covered + genuinely missing: stays a critical finding.
	for _, w := range []struct{ kind, ns, name, reason string }{
		{"Ingress", "prod", "prod-ing", "Missing backend Service"},
		{"Pod", "prod", "prod-pod", "Missing ConfigMap"},
	} {
		if !findProblem(problems, w.kind, w.ns, w.name, w.reason) {
			t.Errorf("covered-namespace missing ref not flagged: %+v\ngot: %+v", w, problems)
		}
	}
	for _, p := range problems {
		if p.Namespace == "prod" && hasPrefix(p.Reason, "Missing") && p.Severity != "critical" && p.Reason != "Missing TLS Secret" {
			t.Errorf("covered missing ref should stay critical: %+v", p)
		}
	}

	// Uncovered namespace: every staging source must be silent — no matter
	// whether its target actually exists there.
	for _, p := range problems {
		if p.Namespace == "staging" {
			t.Errorf("staging (uncovered target informers) must produce NO findings, got: %+v", p)
		}
	}

	// Covered + present: no finding.
	for _, p := range problems {
		if p.Name == "prod-ing-ok" {
			t.Errorf("existing covered target must not flag: %+v", p)
		}
	}
}

// TestDetectMissingGatewayRefsCrossNamespaceCoverage: a Gateway route
// backendRef into a namespace the Services informer doesn't watch is exactly
// the cross-namespace blind spot — the per-namespace lister returns NotFound
// for "platform" even when the Service exists there. Must be silent, while a
// same-namespace (covered) genuine miss keeps flagging.
func TestDetectMissingGatewayRefsCrossNamespaceCoverage(t *testing.T) {
	defer ResetTestDynamicState()

	now := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	// Exists in platform, but the Services informer is scoped to prod.
	platformSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "platform", CreationTimestamp: now},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	client := fake.NewClientset(platformSvc)
	cache := scopedRefTestCache(t, client, map[string]k8score.ResourceScope{
		"services": {Enabled: true, Namespace: "prod"},
	})

	routeGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{routeGVR: "HTTPRouteList"},
		gatewayRoute("cross-ns", "prod", now, []any{
			// Cross-namespace target the cache can't observe: silent.
			map[string]any{"name": "shared", "namespace": "platform", "port": int64(8080)},
			// Same-namespace genuine miss: covered, must flag.
			map[string]any{"name": "missing-in-prod", "port": int64(80)},
		}),
	)
	if err := InitTestDynamicResourceCache(dynClient, []APIResource{
		{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute", Name: "httproutes", Verbs: []string{"list", "watch"}},
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

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectMissingGatewayRefs(cache, dynCache, GetResourceDiscovery(), "")
		if findProblem(problems, "HTTPRoute", "prod", "cross-ns", "Missing Gateway backend Service") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !findProblem(problems, "HTTPRoute", "prod", "cross-ns", "Missing Gateway backend Service") {
		t.Fatalf("covered same-namespace miss must keep flagging: %+v", problems)
	}
	for _, p := range problems {
		if hasSubstr(p.Message, "platform") || hasSubstr(p.Message, "shared") {
			t.Errorf("cross-namespace backendRef into an uncovered namespace must be silent: %+v", p)
		}
	}
}
