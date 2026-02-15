package context

import (
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestMinifyResource_Pod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "my-pod",
			Namespace:       "default",
			UID:             "abc-123-def",
			ResourceVersion: "12345",
			Generation:      3,
			Labels:          map[string]string{"app": "web"},
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": `{"big":"json"}`,
				"kubernetes.io/ingress.class":                     "nginx",
				"some-random-annotation":                          "value",
			},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "my-rs"},
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "nginx:1.25",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
					TerminationMessagePath:   "/dev/termination-log",
					TerminationMessagePolicy: "File",
					ImagePullPolicy:          "IfNotPresent",
					VolumeMounts: []corev1.VolumeMount{
						{Name: "config", MountPath: "/etc/config"},
						{Name: "data", MountPath: "/data"},
					},
				},
			},
			DNSPolicy:     "ClusterFirst",
			SchedulerName: "default-scheduler",
			Tolerations: []corev1.Toleration{
				{Key: "node.kubernetes.io/not-ready"},
			},
		},
		Status: corev1.PodStatus{
			Phase: "Running",
			Conditions: []corev1.PodCondition{
				{Type: "Ready", Status: "True"},
			},
		},
	}

	result, err := MinifyResource(pod)
	if err != nil {
		t.Fatalf("MinifyResource failed: %v", err)
	}

	// Should have metadata
	meta, ok := result["metadata"].(map[string]any)
	if !ok {
		t.Fatal("Expected metadata map")
	}

	// Should keep name, namespace, labels, ownerReferences
	if meta["name"] != "my-pod" {
		t.Errorf("Expected name=my-pod, got %v", meta["name"])
	}
	if meta["namespace"] != "default" {
		t.Errorf("Expected namespace=default, got %v", meta["namespace"])
	}

	// Should strip uid, resourceVersion, generation
	if _, exists := meta["uid"]; exists {
		t.Error("uid should be stripped")
	}
	if _, exists := meta["resourceVersion"]; exists {
		t.Error("resourceVersion should be stripped")
	}
	if _, exists := meta["generation"]; exists {
		t.Error("generation should be stripped")
	}

	// Should keep ingress annotation, strip random ones
	annotations, _ := meta["annotations"].(map[string]any)
	if annotations == nil {
		t.Fatal("Expected annotations to exist (ingress.class should be kept)")
	}
	if _, ok := annotations["kubernetes.io/ingress.class"]; !ok {
		t.Error("Should keep kubernetes.io/ingress.class annotation")
	}
	if _, ok := annotations["some-random-annotation"]; ok {
		t.Error("Should strip random annotations")
	}
	if _, ok := annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
		t.Error("Should strip last-applied-configuration")
	}

	// Should strip tolerations, dnsPolicy, schedulerName from spec
	spec, _ := result["spec"].(map[string]any)
	if spec == nil {
		t.Fatal("Expected spec")
	}
	if _, exists := spec["tolerations"]; exists {
		t.Error("tolerations should be stripped")
	}
	if _, exists := spec["dnsPolicy"]; exists {
		t.Error("dnsPolicy should be stripped")
	}
	if _, exists := spec["schedulerName"]; exists {
		t.Error("schedulerName should be stripped")
	}

	// Should strip terminationMessagePath from containers
	containers, _ := spec["containers"].([]any)
	if len(containers) == 0 {
		t.Fatal("Expected containers")
	}
	container, _ := containers[0].(map[string]any)
	if _, exists := container["terminationMessagePath"]; exists {
		t.Error("terminationMessagePath should be stripped")
	}
	if _, exists := container["terminationMessagePolicy"]; exists {
		t.Error("terminationMessagePolicy should be stripped")
	}

	// imagePullPolicy "IfNotPresent" should be stripped (only keep "Never")
	if _, exists := container["imagePullPolicy"]; exists {
		t.Error("imagePullPolicy=IfNotPresent should be stripped")
	}

	// volumeMounts should be simplified to names only
	switch mounts := container["volumeMounts"].(type) {
	case []string:
		if len(mounts) != 2 {
			t.Errorf("Expected 2 volume mount names, got %v", mounts)
		}
	case []any:
		if len(mounts) != 2 {
			t.Errorf("Expected 2 volume mount names, got %v", mounts)
		}
	default:
		t.Errorf("Expected volume mount names list, got %T: %v", container["volumeMounts"], container["volumeMounts"])
	}

	// Status should be preserved in full
	status, _ := result["status"].(map[string]any)
	if status == nil {
		t.Fatal("Expected status to be preserved")
	}
	if status["phase"] != "Running" {
		t.Errorf("Expected phase=Running, got %v", status["phase"])
	}
}

func TestMinifyResource_Secret(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db-credentials",
			Namespace: "default",
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"username": []byte("admin"),
			"password": []byte("supersecret123"),
		},
	}

	result, err := MinifyResource(secret)
	if err != nil {
		t.Fatalf("MinifyResource failed: %v", err)
	}

	// Should never contain actual secret data
	data, _ := json.Marshal(result)
	if contains(string(data), "admin") || contains(string(data), "supersecret") {
		t.Errorf("Secret data leaked in minified output: %s", string(data))
	}

	// Should contain metadata
	if result["name"] != "db-credentials" {
		t.Errorf("Expected name=db-credentials, got %v", result["name"])
	}
	if result["kind"] != "Secret" {
		t.Errorf("Expected kind=Secret, got %v", result["kind"])
	}

	// Should list keys
	keys, ok := result["keys"].([]string)
	if !ok {
		t.Fatal("Expected keys list")
	}
	if len(keys) != 2 {
		t.Errorf("Expected 2 keys, got %d", len(keys))
	}
}

func TestMinifyResource_Compact_EnvSimplifiedToNames(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "myapp:latest",
					Env: []corev1.EnvVar{
						{Name: "API_KEY", Value: "sk-abc123def456ghi789jkl012mno345pqr678stu901"},
						{Name: "APP_ENV", Value: "production"},
					},
				},
			},
		},
	}

	// Compact level (via backward-compat MinifyResource): env simplified to names only
	result, err := MinifyResource(pod)
	if err != nil {
		t.Fatalf("MinifyResource failed: %v", err)
	}

	data, _ := json.Marshal(result)
	output := string(data)

	if contains(output, "sk-abc123") {
		t.Errorf("API key should not appear at compact level: %s", output)
	}
	if contains(output, "production") {
		t.Errorf("Env values should be stripped at compact level (names only): %s", output)
	}
	if !contains(output, "API_KEY") || !contains(output, "APP_ENV") {
		t.Errorf("Env names should be preserved at compact level: %s", output)
	}
}

func TestMinify_Detail_EnvValueRedaction(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "myapp:latest",
					Env: []corev1.EnvVar{
						{Name: "API_KEY", Value: "sk-abc123def456ghi789jkl012mno345pqr678stu901"},
						{Name: "APP_ENV", Value: "production"},
					},
				},
			},
		},
	}

	// Detail level: env values preserved but secrets redacted
	raw, err := Minify(pod, LevelDetail)
	if err != nil {
		t.Fatalf("Minify failed: %v", err)
	}

	data, _ := json.Marshal(raw)
	output := string(data)

	if contains(output, "sk-abc123") {
		t.Errorf("API key in env value not redacted at detail level: %s", output)
	}
	if !contains(output, "production") {
		t.Errorf("Safe env value should be preserved at detail level: %s", output)
	}
}

func TestMinifyUnstructured_Compact(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]any{
				"name":            "my-app",
				"namespace":       "argocd",
				"uid":             "should-be-stripped",
				"resourceVersion": "99999",
				"annotations": map[string]any{
					"argocd.argoproj.io/sync-wave": "1",
					"random-annotation":            "remove-me",
				},
			},
			"spec": map[string]any{
				"source": map[string]any{
					"repoURL": "https://github.com/org/repo",
				},
			},
			"status": map[string]any{
				"sync": map[string]any{
					"status": "Synced",
				},
				"health": map[string]any{
					"status": "Healthy",
				},
			},
		},
	}

	// Test backward-compat wrapper (Compact level)
	result := MinifyResourceUnstructured(obj)

	meta, _ := result["metadata"].(map[string]any)
	if meta == nil {
		t.Fatal("Expected metadata")
	}
	if _, exists := meta["uid"]; exists {
		t.Error("uid should be stripped")
	}
	if _, exists := meta["resourceVersion"]; exists {
		t.Error("resourceVersion should be stripped")
	}

	// ArgoCD annotation should be kept at compact level
	annotations, _ := meta["annotations"].(map[string]any)
	if _, ok := annotations["argocd.argoproj.io/sync-wave"]; !ok {
		t.Error("ArgoCD annotation should be kept")
	}
	if _, ok := annotations["random-annotation"]; ok {
		t.Error("Random annotation should be stripped")
	}

	// Status should be preserved
	if result["status"] == nil {
		t.Error("Status should be preserved")
	}
}

func TestMinifyUnstructured_Detail(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]any{
				"name":            "my-app",
				"namespace":       "argocd",
				"uid":             "should-be-stripped",
				"resourceVersion": "99999",
				"annotations": map[string]any{
					"argocd.argoproj.io/sync-wave": "1",
					"random-annotation":            "kept-at-detail",
				},
			},
			"spec": map[string]any{
				"source": map[string]any{
					"repoURL": "https://github.com/org/repo",
				},
			},
			"status": map[string]any{
				"sync": map[string]any{
					"status": "Synced",
				},
			},
		},
	}

	raw := MinifyUnstructured(obj, LevelDetail)
	result, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("Expected map[string]any, got %T", raw)
	}

	meta, _ := result["metadata"].(map[string]any)
	if meta == nil {
		t.Fatal("Expected metadata")
	}
	if _, exists := meta["uid"]; exists {
		t.Error("uid should be stripped at detail level")
	}

	// At Detail level, ALL annotations are kept
	annotations, _ := meta["annotations"].(map[string]any)
	if _, ok := annotations["random-annotation"]; !ok {
		t.Error("All annotations should be kept at detail level")
	}
}

func TestMinify_StatusPresentAtAllLevels(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "status-test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx:1.25"},
			},
		},
		Status: corev1.PodStatus{
			Phase: "Running",
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: true, RestartCount: 0},
			},
			Conditions: []corev1.PodCondition{
				{Type: "Ready", Status: "True"},
			},
		},
	}

	// Summary: status as a field
	raw, err := Minify(pod, LevelSummary)
	if err != nil {
		t.Fatalf("Minify(Summary) failed: %v", err)
	}
	summary, ok := raw.(*ResourceSummary)
	if !ok {
		t.Fatalf("Expected *ResourceSummary, got %T", raw)
	}
	if summary.Status != "Running" {
		t.Errorf("Summary: expected status=Running, got %s", summary.Status)
	}

	// Detail: status block present
	rawDetail, err := Minify(pod, LevelDetail)
	if err != nil {
		t.Fatalf("Minify(Detail) failed: %v", err)
	}
	detailMap := rawDetail.(map[string]any)
	if detailMap["status"] == nil {
		t.Error("Detail: status should be present")
	}

	// Compact: status block present
	rawCompact, err := Minify(pod, LevelCompact)
	if err != nil {
		t.Fatalf("Minify(Compact) failed: %v", err)
	}
	compactMap := rawCompact.(map[string]any)
	if compactMap["status"] == nil {
		t.Error("Compact: status should be present")
	}
}

func TestMinify_SecretNeverLeaksAtAnyLevel(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "secret-test",
			Namespace: "default",
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"password": []byte("s3cr3t-value"),
		},
		StringData: map[string]string{
			"api-key": "another-secret",
		},
	}

	for _, level := range []VerbosityLevel{LevelSummary, LevelCompact, LevelDetail} {
		raw, err := Minify(secret, level)
		if err != nil {
			t.Fatalf("Minify(level=%d) failed: %v", level, err)
		}
		data, _ := json.Marshal(raw)
		output := string(data)
		if contains(output, "s3cr3t-value") || contains(output, "another-secret") {
			t.Errorf("Level %d: secret data leaked: %s", level, output)
		}
	}
}

func TestMinify_DetailKeepsAllAnnotations(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "detail-test",
			Namespace: "default",
			Annotations: map[string]string{
				"custom.example.com/note": "important",
				"kubernetes.io/ingress.class": "nginx",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx:1.25"},
			},
		},
	}

	raw, err := Minify(pod, LevelDetail)
	if err != nil {
		t.Fatalf("Minify(Detail) failed: %v", err)
	}
	result := raw.(map[string]any)
	meta := result["metadata"].(map[string]any)
	annotations := meta["annotations"].(map[string]any)

	if _, ok := annotations["custom.example.com/note"]; !ok {
		t.Error("Detail should keep all annotations including custom ones")
	}
}

func TestMinify_CompactStripsCustomAnnotations(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "compact-test",
			Namespace: "default",
			Annotations: map[string]string{
				"custom.example.com/note": "important",
				"kubernetes.io/ingress.class": "nginx",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx:1.25"},
			},
		},
	}

	raw, err := Minify(pod, LevelCompact)
	if err != nil {
		t.Fatalf("Minify(Compact) failed: %v", err)
	}
	result := raw.(map[string]any)
	meta := result["metadata"].(map[string]any)
	annotations, _ := meta["annotations"].(map[string]any)

	if _, ok := annotations["custom.example.com/note"]; ok {
		t.Error("Compact should strip custom annotations")
	}
	if _, ok := annotations["kubernetes.io/ingress.class"]; !ok {
		t.Error("Compact should keep known prefix annotations")
	}
}

func TestMinify_CompactStripsCommandAndProbes(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "compact-spec-test",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "app",
					Image:   "nginx:1.25",
					Command: []string{"/bin/sh", "-c", "echo hello"},
					Args:    []string{"--port=8080"},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: "/health"},
						},
					},
				},
			},
		},
	}

	raw, err := Minify(pod, LevelCompact)
	if err != nil {
		t.Fatalf("Minify(Compact) failed: %v", err)
	}

	data, _ := json.Marshal(raw)
	output := string(data)

	if contains(output, "echo hello") {
		t.Errorf("Compact should strip command: %s", output)
	}
	if contains(output, "--port=8080") {
		t.Errorf("Compact should strip args: %s", output)
	}
	if contains(output, "livenessProbe") || contains(output, "/health") {
		t.Errorf("Compact should strip probes: %s", output)
	}
	if !contains(output, "nginx:1.25") {
		t.Errorf("Compact should keep image: %s", output)
	}
}

func TestMinify_DetailKeepsCommandAndProbes(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "detail-spec-test",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "app",
					Image:   "nginx:1.25",
					Command: []string{"/bin/sh", "-c", "echo hello"},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: "/health"},
						},
					},
				},
			},
		},
	}

	raw, err := Minify(pod, LevelDetail)
	if err != nil {
		t.Fatalf("Minify(Detail) failed: %v", err)
	}

	data, _ := json.Marshal(raw)
	output := string(data)

	if !contains(output, "echo hello") {
		t.Errorf("Detail should keep command: %s", output)
	}
	if !contains(output, "/health") {
		t.Errorf("Detail should keep probes: %s", output)
	}
}

func TestMinifyList(t *testing.T) {
	objs := []runtime.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
			},
			Status: corev1.PodStatus{Phase: "Running"},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-2", Namespace: "default"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "redis"}},
			},
			Status: corev1.PodStatus{Phase: "Pending"},
		},
	}

	results, err := MinifyList(objs, LevelSummary)
	if err != nil {
		t.Fatalf("MinifyList failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	s1, ok := results[0].(*ResourceSummary)
	if !ok {
		t.Fatalf("Expected *ResourceSummary, got %T", results[0])
	}
	if s1.Name != "pod-1" {
		t.Errorf("Expected name=pod-1, got %s", s1.Name)
	}
}

func TestBudgetMode_ResourceVerbosity(t *testing.T) {
	if BudgetLocal.ResourceVerbosity() != LevelCompact {
		t.Error("BudgetLocal should use LevelCompact")
	}
	if BudgetCloud.ResourceVerbosity() != LevelDetail {
		t.Error("BudgetCloud should use LevelDetail")
	}
}

func TestMinifyUnstructured_Summary(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]any{
				"name":              "my-app",
				"namespace":         "argocd",
				"creationTimestamp": "2024-01-01T00:00:00Z",
			},
			"spec": map[string]any{
				"source": map[string]any{
					"repoURL": "https://github.com/org/repo",
				},
			},
			"status": map[string]any{
				"sync": map[string]any{
					"status": "OutOfSync",
				},
				"health": map[string]any{
					"status": "Degraded",
				},
			},
		},
	}

	raw := MinifyUnstructured(obj, LevelSummary)
	summary, ok := raw.(*ResourceSummary)
	if !ok {
		t.Fatalf("Expected *ResourceSummary, got %T", raw)
	}

	if summary.Name != "my-app" {
		t.Errorf("Expected name=my-app, got %s", summary.Name)
	}
	if summary.Status != "OutOfSync" {
		t.Errorf("Expected status=OutOfSync, got %s", summary.Status)
	}
	if summary.Issue != "Degraded" {
		t.Errorf("Expected issue=Degraded, got %s", summary.Issue)
	}
}

func TestMinify_DetailPrunesPodStatusNoise(t *testing.T) {
	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Name: "prune-test", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25"}},
		},
		Status: corev1.PodStatus{
			Phase:  "Running",
			HostIP: "10.0.0.1",
			PodIP:  "10.244.0.5",
			PodIPs: []corev1.PodIP{{IP: "10.244.0.5"}},
			Conditions: []corev1.PodCondition{
				{Type: "Ready", Status: "True"},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: true, RestartCount: 0},
			},
		},
	}

	raw, err := Minify(pod, LevelDetail)
	if err != nil {
		t.Fatalf("Minify(Detail) failed: %v", err)
	}
	result := raw.(map[string]any)
	status := result["status"].(map[string]any)

	// Should be stripped
	for _, key := range []string{"hostIP", "podIP", "podIPs", "qosClass", "startTime"} {
		if _, exists := status[key]; exists {
			t.Errorf("%s should be stripped from Pod status at Detail level", key)
		}
	}

	// Should be kept
	if status["phase"] == nil {
		t.Error("phase should be kept in Pod status")
	}
	if status["conditions"] == nil {
		t.Error("conditions should be kept in Pod status")
	}
	if status["containerStatuses"] == nil {
		t.Error("containerStatuses should be kept in Pod status")
	}
}

func TestMinify_DetailPrunesWorkloadStatusNoise(t *testing.T) {
	dep := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"},
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25"}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:           3,
			ReadyReplicas:      3,
			ObservedGeneration: 5,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: "True"},
			},
		},
	}

	raw, err := Minify(dep, LevelDetail)
	if err != nil {
		t.Fatalf("Minify(Detail) failed: %v", err)
	}
	result := raw.(map[string]any)
	status := result["status"].(map[string]any)

	// Should be stripped
	if _, exists := status["observedGeneration"]; exists {
		t.Error("observedGeneration should be stripped from Deployment status at Detail level")
	}

	// Should be kept
	if status["replicas"] == nil {
		t.Error("replicas should be kept in Deployment status")
	}
	if status["readyReplicas"] == nil {
		t.Error("readyReplicas should be kept in Deployment status")
	}
	if status["conditions"] == nil {
		t.Error("conditions should be kept in Deployment status")
	}
}
