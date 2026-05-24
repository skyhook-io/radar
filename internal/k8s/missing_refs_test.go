package k8s

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	// Ingress with missing backend + existing default backend (must not double-flag)
	ingMixed := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "ing", Namespace: "prod", CreationTimestamp: now},
		Spec: networkingv1.IngressSpec{
			DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "real-svc"}},
			Rules: []networkingv1.IngressRule{{
				Host: "app.example.com",
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{Path: "/api", Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "missing-svc"}}}},
				}},
			}},
		},
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
		ingMixed,
		pvcExplicitMissingSC, pvcExistingSC, pvcDefaultSC,
		rbMissing, rbExisting, crbMissing, crbExisting,
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	// Wait for informers to populate before asserting.
	deadline := time.Now().Add(2 * time.Second)
	var problems []Problem
	for time.Now().Before(deadline) {
		problems = DetectMissingRefs(cache, "")
		if len(problems) >= 9 {
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
		{"HorizontalPodAutoscaler", "prod", "hpa-bad", "Missing scaleTargetRef"},
		{"Ingress", "prod", "ing", "Missing backend Service"},
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
}

// --- helpers ---

func findProblem(ps []Problem, kind, ns, name, reason string) bool {
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
