package k8s

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

// TestDetectProblems_PopulatesGroup pins that every built-in Problem
// emitted by DetectProblems carries the correct canonical API group.
//
// The summary_context issue index keys per-resource counts as
// "group|kind|ns|name" — a Problem with an empty Group collides with
// no real bucket, silently zeroing issueCount for that workload row.
// Pre-fix, all the built-in append-Problem sites omitted the field, so
// every broken Deployment/StatefulSet/DaemonSet/HPA/CronJob/Job
// reported issueCount: 0 in the AI list envelope — a regression
// against the pre-group-aware behavior.
//
// Construct one broken object per built-in kind, drive DetectProblems
// against a fake client, and assert each emitted Problem's Group
// matches the canonical group for its kind.
func TestDetectProblems_PopulatesGroup(t *testing.T) {
	defer ResetTestState()

	oneReplica := int32(1)
	minReplicas := int32(1)
	now := time.Now()
	// Job needs to be older than 1h to surface a "stuck" problem.
	jobStart := metav1.NewTime(now.Add(-2 * time.Hour))

	client := fake.NewClientset(
		// Deployment with unavailable replicas — triggers the
		// "X/Y available" Problem branch.
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
			Spec:       appsv1.DeploymentSpec{Replicas: &oneReplica},
			Status: appsv1.DeploymentStatus{
				Replicas:            1,
				UnavailableReplicas: 1,
			},
		},
		// StatefulSet with readyReplicas < replicas.
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"},
			Spec:       appsv1.StatefulSetSpec{Replicas: &oneReplica},
			Status: appsv1.StatefulSetStatus{
				Replicas:      1,
				ReadyReplicas: 0,
			},
		},
		// DaemonSet with numberUnavailable > 0.
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "logger", Namespace: "prod"},
			Status: appsv1.DaemonSetStatus{
				NumberUnavailable: 2,
			},
		},
		// HPA capped by maxReplicas — DetectHPAProblems flags
		// "maxed" when the controller reports TooManyReplicas.
		// The wrapper sets Group="autoscaling".
		&autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				MinReplicas: &minReplicas,
				MaxReplicas: 10,
			},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{
				CurrentReplicas: 10,
				DesiredReplicas: 10,
				Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
					{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionTrue, Reason: "TooManyReplicas", Message: "the desired replica count is more than the maximum replica count"},
				},
			},
		},
		// Job stuck Active>0 for >1h with no completions.
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "prod", CreationTimestamp: jobStart},
			Status: batchv1.JobStatus{
				Active:    1,
				Succeeded: 0,
				Failed:    0,
			},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	// Allow informers a brief moment to populate. The fake clientset
	// pre-seeds the store, but the lister types reconstruct via
	// informer events on a separate goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasAllProblemTypes(DetectProblems(cache, "prod")) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	problems := DetectProblems(cache, "prod")

	wantGroup := map[string]string{
		"Deployment":              "apps",
		"StatefulSet":             "apps",
		"DaemonSet":               "apps",
		"HorizontalPodAutoscaler": "autoscaling",
		"Job":                     "batch",
	}

	got := make(map[string]string, len(problems))
	for _, p := range problems {
		// One Problem per kind is enough for the Group assertion;
		// duplicates (e.g. Deployment Available + ProgressDeadline)
		// must agree on Group so the last-write-wins shape is fine.
		got[p.Kind] = p.Group
	}

	for kind, want := range wantGroup {
		gotGroup, ok := got[kind]
		if !ok {
			t.Errorf("no Problem emitted for %s — fixture wiring broken; got %d problems: %+v", kind, len(problems), problems)
			continue
		}
		if gotGroup != want {
			t.Errorf("%s.Group = %q, want %q (summary_context index keys by group — empty Group zeros issueCount)", kind, gotGroup, want)
		}
	}
}

func hasAllProblemTypes(problems []Detection) bool {
	seen := map[string]bool{}
	for _, p := range problems {
		seen[p.Kind] = true
	}
	return seen["Deployment"] && seen["StatefulSet"] && seen["DaemonSet"] && seen["HorizontalPodAutoscaler"] && seen["Job"]
}

func TestDetectProblems_ConfigSignals(t *testing.T) {
	t.Run("coredns service nxdomain override", func(t *testing.T) {
		defer ResetTestState()

		client := fake.NewClientset(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system", CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Minute))},
			Data: map[string]string{
				"Corefile": `template IN A product-catalog.astronomy-shop.svc.cluster.local {
  rcode NXDOMAIN
}`,
			},
		})
		if err := InitTestResourceCache(client); err != nil {
			t.Fatalf("InitTestResourceCache: %v", err)
		}

		for _, p := range DetectProblems(GetResourceCache(), "astronomy-shop") {
			if p.Reason == "CoreDNS NXDOMAIN override" {
				t.Fatalf("namespace-scoped DetectProblems leaked CoreDNS issue: %+v", p)
			}
		}

		p := waitForProblem(t, "", "CoreDNS NXDOMAIN override")
		if p.Kind != "ConfigMap" || p.Namespace != "kube-system" || p.Name != "coredns" {
			t.Fatalf("CoreDNS problem subject = %s/%s/%s, want ConfigMap/kube-system/coredns: %+v", p.Kind, p.Namespace, p.Name, p)
		}
		if p.Severity != "warning" {
			t.Fatalf("CoreDNS severity = %q, want warning", p.Severity)
		}
	})

	t.Run("coredns service rewrite", func(t *testing.T) {
		defer ResetTestState()

		client := fake.NewClientset(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system", CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Minute))},
			Data: map[string]string{
				"Corefile": `rewrite name product-catalog.astronomy-shop.svc.cluster.local blackhole.svc.cluster.local`,
			},
		})
		if err := InitTestResourceCache(client); err != nil {
			t.Fatalf("InitTestResourceCache: %v", err)
		}

		p := waitForProblem(t, "", "CoreDNS service DNS rewrite")
		if p.Kind != "ConfigMap" || p.Namespace != "kube-system" || p.Name != "coredns" {
			t.Fatalf("CoreDNS rewrite problem subject = %s/%s/%s, want ConfigMap/kube-system/coredns: %+v", p.Kind, p.Namespace, p.Name, p)
		}
	})

	t.Run("env service port mismatch is context when workload is healthy", func(t *testing.T) {
		defer ResetTestState()

		replicas := int32(1)
		client := fake.NewClientset(
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "prod", CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Minute))},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: "app",
						Env: []corev1.EnvVar{{
							Name:  "PRODUCT_CATALOG_ADDR",
							Value: "redis://product-catalog:8082/cache?token=super-secret",
						}},
					}}}},
				},
				Status: appsv1.DeploymentStatus{
					AvailableReplicas: 1,
					Conditions: []appsv1.DeploymentCondition{{
						Type:   appsv1.DeploymentAvailable,
						Status: corev1.ConditionTrue,
					}},
				},
			},
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "product-catalog", Namespace: "prod"},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{Port: 8080}},
				},
			},
		)
		if err := InitTestResourceCache(client); err != nil {
			t.Fatalf("InitTestResourceCache: %v", err)
		}

		check := waitForEnvServiceCheck(t, "prod", "port_mismatch")
		if check.WorkloadKind != "Deployment" || check.WorkloadName != "frontend" || !strings.Contains(check.Message, "product-catalog:8082") || !strings.Contains(check.Message, "8080") {
			t.Fatalf("env Service mismatch context = %+v", check)
		}
		if check.Value != "product-catalog:8082" || strings.Contains(check.Value, "super-secret") {
			t.Fatalf("env Service check value = %q, want parsed host:port without query secret", check.Value)
		}
		for _, p := range DetectProblems(GetResourceCache(), "prod") {
			if p.Reason == "Service port mismatch" {
				t.Fatalf("healthy workload should not promote env Service mismatch to Issue: %+v", p)
			}
		}
	})

	t.Run("env service port mismatch becomes issue when workload is degraded", func(t *testing.T) {
		defer ResetTestState()

		replicas := int32(1)
		client := fake.NewClientset(
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "prod", CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Minute))},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: "app",
						Env: []corev1.EnvVar{{
							Name:  "PRODUCT_CATALOG_ADDR",
							Value: "product-catalog:8082",
						}},
					}}}},
				},
				Status: appsv1.DeploymentStatus{
					UnavailableReplicas: 1,
					Conditions: []appsv1.DeploymentCondition{{
						Type:   appsv1.DeploymentAvailable,
						Status: corev1.ConditionFalse,
					}},
				},
			},
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "product-catalog", Namespace: "prod"},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{Port: 8080}},
				},
			},
		)
		if err := InitTestResourceCache(client); err != nil {
			t.Fatalf("InitTestResourceCache: %v", err)
		}

		p := waitForProblem(t, "prod", "Service port mismatch")
		if p.Kind != "Deployment" || p.Name != "frontend" || !strings.Contains(p.Message, "product-catalog:8082") || !strings.Contains(p.Message, "8080") {
			t.Fatalf("env Service mismatch problem = %+v", p)
		}
	})

	t.Run("cronjob env service port mismatch becomes issue when owned job failed", func(t *testing.T) {
		defer ResetTestState()

		controller := true
		blockOwnerDeletion := true
		client := fake.NewClientset(
			&batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{Name: "sync", Namespace: "prod", UID: "cronjob-1", CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Minute))},
				Spec: batchv1.CronJobSpec{
					JobTemplate: batchv1.JobTemplateSpec{
						Spec: batchv1.JobSpec{
							Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{{
									Name: "app",
									Env: []corev1.EnvVar{{
										Name:  "PRODUCT_CATALOG_ADDR",
										Value: "product-catalog:8082",
									}},
								}},
							}},
						},
					},
				},
			},
			&batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sync-123",
					Namespace: "prod",
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion:         "batch/v1",
						Kind:               "CronJob",
						Name:               "sync",
						UID:                "cronjob-1",
						Controller:         &controller,
						BlockOwnerDeletion: &blockOwnerDeletion,
					}},
				},
				Status: batchv1.JobStatus{
					Failed: 1,
					Conditions: []batchv1.JobCondition{{
						Type:   batchv1.JobFailed,
						Status: corev1.ConditionTrue,
					}},
				},
			},
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "product-catalog", Namespace: "prod"},
				Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
			},
		)
		if err := InitTestResourceCache(client); err != nil {
			t.Fatalf("InitTestResourceCache: %v", err)
		}

		p := waitForProblem(t, "prod", "Service port mismatch")
		if p.Kind != "CronJob" || p.Name != "sync" || !strings.Contains(p.Message, "product-catalog:8082") {
			t.Fatalf("CronJob env Service mismatch problem = %+v", p)
		}
	})

	t.Run("missing env service ref becomes warning issue even when caller is healthy", func(t *testing.T) {
		defer ResetTestState()

		replicas := int32(1)
		client := fake.NewClientset(&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "prod", CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Minute))},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "app",
					Env: []corev1.EnvVar{{
						Name:  "AD_SERVICE_ADDR",
						Value: "ad:8080",
					}},
				}}}},
			},
			Status: appsv1.DeploymentStatus{
				AvailableReplicas: 1,
				Conditions: []appsv1.DeploymentCondition{{
					Type:   appsv1.DeploymentAvailable,
					Status: corev1.ConditionTrue,
				}},
			},
		})
		if err := InitTestResourceCache(client); err != nil {
			t.Fatalf("InitTestResourceCache: %v", err)
		}

		p := waitForProblem(t, "prod", "Missing referenced Service")
		if p.Kind != "Deployment" || p.Name != "frontend" || p.Severity != "warning" || !strings.Contains(p.Message, "Service/ad does not exist") {
			t.Fatalf("missing env Service problem = %+v", p)
		}
	})

	t.Run("cross namespace env service ref is not promoted to issue", func(t *testing.T) {
		defer ResetTestState()

		replicas := int32(1)
		client := fake.NewClientset(&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "prod", CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Minute))},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "app",
					Env: []corev1.EnvVar{{
						Name:  "PRODUCT_CATALOG_ADDR",
						Value: "product-catalog.shared.svc.cluster.local:8082",
					}},
				}}}},
			},
			Status: appsv1.DeploymentStatus{
				UnavailableReplicas: 1,
				Conditions: []appsv1.DeploymentCondition{{
					Type:   appsv1.DeploymentAvailable,
					Status: corev1.ConditionFalse,
				}},
			},
		})
		if err := InitTestResourceCache(client); err != nil {
			t.Fatalf("InitTestResourceCache: %v", err)
		}

		check := waitForEnvServiceCheck(t, "prod", "cross_namespace_unverified")
		if check.ServiceNamespace != "shared" || check.ServiceName != "product-catalog" || check.ServicePorts != nil {
			t.Fatalf("cross-namespace env Service check leaked target details: %+v", check)
		}
		for _, p := range DetectProblems(GetResourceCache(), "prod") {
			if p.Reason == "Service port mismatch" || p.Reason == "Missing referenced Service" {
				t.Fatalf("cross-namespace env Service check should not promote to Issue: %+v", p)
			}
		}
	})
}

func waitForEnvServiceCheck(t *testing.T, namespace, status string) EnvServiceRefCheck {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var checks []EnvServiceRefCheck
	for time.Now().Before(deadline) {
		checks = FindEnvServiceRefChecks(GetResourceCache(), namespace)
		for _, c := range checks {
			if c.Status == status {
				return c
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("env Service check status %q not found in namespace %q; got %+v", status, namespace, checks)
	return EnvServiceRefCheck{}
}

func waitForProblem(t *testing.T, namespace, reason string) Detection {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(GetResourceCache(), namespace)
		for _, p := range problems {
			if p.Reason == reason {
				return p
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("problem reason %q not found in namespace %q; got %+v", reason, namespace, problems)
	return Detection{}
}

func TestParseEnvServiceRefTrimsSuffixes(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		wantName  string
		wantNS    string
		wantPort  int32
		wantFound bool
	}{
		{
			name:      "plain path",
			value:     "product-catalog:8080/health",
			wantName:  "product-catalog",
			wantNS:    "prod",
			wantPort:  8080,
			wantFound: true,
		},
		{
			name:      "query before path",
			value:     "product-catalog:8080?ready=true/extra",
			wantName:  "product-catalog",
			wantNS:    "prod",
			wantPort:  8080,
			wantFound: true,
		},
		{
			name:      "fragment",
			value:     "product-catalog.prod.svc.cluster.local:8080#main",
			wantName:  "product-catalog",
			wantNS:    "prod",
			wantPort:  8080,
			wantFound: true,
		},
		{
			name:      "two part same namespace rejected as ambiguous external host",
			value:     "product-catalog.prod:8080",
			wantFound: false,
		},
		{
			name:      "two part other namespace rejected as ambiguous external host",
			value:     "product-catalog.shared:8080",
			wantFound: false,
		},
		{
			name:      "external two label host rejected",
			value:     "api.github:8080",
			wantFound: false,
		},
		{
			name:      "url scheme uses host",
			value:     "http://product-catalog.prod.svc.cluster.local:8080/health?ready=true",
			wantName:  "product-catalog",
			wantNS:    "prod",
			wantPort:  8080,
			wantFound: true,
		},
		{
			name:      "bad port",
			value:     "product-catalog:abc/health",
			wantFound: false,
		},
		{
			name:      "ip literal",
			value:     "10.0.0.5:8080",
			wantFound: false,
		},
		{
			name:      "localhost rejected",
			value:     "localhost:8080",
			wantFound: false,
		},
		{
			name:      "localhost url rejected",
			value:     "http://localhost:3000/health",
			wantFound: false,
		},
		{
			name:      "localhost trailing dot rejected",
			value:     "LOCALHOST.:9090",
			wantFound: false,
		},
		{
			name:      "three part non service dns",
			value:     "product-catalog.prod.example:8080",
			wantFound: false,
		},
		{
			name:      "external svc tld rejected",
			value:     "foo.bar.svc.example.com:8080",
			wantFound: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseEnvServiceRef(tc.value, "prod")
			if ok != tc.wantFound {
				t.Fatalf("parseEnvServiceRef(%q) ok = %v, want %v; got %+v", tc.value, ok, tc.wantFound, got)
			}
			if !tc.wantFound {
				return
			}
			if got.name != tc.wantName || got.namespace != tc.wantNS || got.port != tc.wantPort {
				t.Fatalf("parseEnvServiceRef(%q) = %+v, want name=%q namespace=%q port=%d", tc.value, got, tc.wantName, tc.wantNS, tc.wantPort)
			}
		})
	}
}

func TestFindEnvServiceRefChecks_SplitHostPort(t *testing.T) {
	// findEnvServiceRefChecks only emits checks for broken references
	// (missing_service, port_mismatch, cross_namespace_unverified). Valid
	// connections are silent. These sub-tests confirm that the _HOST + _PORT
	// pairing correctly synthesises host:port before the resolution step, so
	// broken split-config patterns surface the same way broken combined ones do.

	t.Run("missing service detected via split host+port", func(t *testing.T) {
		defer ResetTestState()
		replicas := int32(1)
		// flagd Service does NOT exist — expect missing_service on FLAGD_HOST.
		client := fake.NewClientset(&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "frontend",
				Namespace:         "prod",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "app",
					Env: []corev1.EnvVar{
						{Name: "FLAGD_HOST", Value: "flagd"},
						{Name: "FLAGD_PORT", Value: "8013"},
						// unpaired _HOST with no port — must not produce a check
						{Name: "ORPHAN_HOST", Value: "some-host"},
					},
				}}}},
			},
			Status: appsv1.DeploymentStatus{
				UnavailableReplicas: 1,
				Conditions:          []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse}},
			},
		})
		if err := InitTestResourceCache(client); err != nil {
			t.Fatalf("InitTestResourceCache: %v", err)
		}
		c := waitForEnvServiceCheck(t, "prod", "missing_service")
		if c.EnvName != "FLAGD_HOST" {
			t.Errorf("EnvName = %q, want FLAGD_HOST", c.EnvName)
		}
		if c.ServiceName != "flagd" {
			t.Errorf("ServiceName = %q, want flagd", c.ServiceName)
		}
		if c.ReferencedPort != 8013 {
			t.Errorf("ReferencedPort = %d, want 8013", c.ReferencedPort)
		}
	})

	t.Run("port mismatch detected via split host+port", func(t *testing.T) {
		defer ResetTestState()
		replicas := int32(1)
		// flagd Service exists but on port 9090, not 8013 — expect port_mismatch.
		client := fake.NewClientset(
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "frontend",
					Namespace:         "prod",
					CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: "app",
						Env: []corev1.EnvVar{
							{Name: "FLAGD_HOST", Value: "flagd"},
							{Name: "FLAGD_PORT", Value: "8013"},
						},
					}}}},
				},
				Status: appsv1.DeploymentStatus{
					UnavailableReplicas: 1,
					Conditions:          []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse}},
				},
			},
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "flagd", Namespace: "prod"},
				Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9090}}},
			},
		)
		if err := InitTestResourceCache(client); err != nil {
			t.Fatalf("InitTestResourceCache: %v", err)
		}
		c := waitForEnvServiceCheck(t, "prod", "port_mismatch")
		if c.EnvName != "FLAGD_HOST" {
			t.Errorf("EnvName = %q, want FLAGD_HOST", c.EnvName)
		}
		if c.ReferencedPort != 8013 {
			t.Errorf("ReferencedPort = %d, want 8013", c.ReferencedPort)
		}
	})

	t.Run("valid split host+port produces no check", func(t *testing.T) {
		defer ResetTestState()
		replicas := int32(1)
		// flagd Service exists on correct port — no check emitted.
		client := fake.NewClientset(
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "frontend",
					Namespace:         "prod",
					CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: "app",
						Env: []corev1.EnvVar{
							{Name: "FLAGD_HOST", Value: "flagd"},
							{Name: "FLAGD_PORT", Value: "8013"},
						},
					}}}},
				},
			},
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "flagd", Namespace: "prod"},
				Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8013}}},
			},
		)
		if err := InitTestResourceCache(client); err != nil {
			t.Fatalf("InitTestResourceCache: %v", err)
		}
		// Give cache time to sync, then assert no checks.
		time.Sleep(200 * time.Millisecond)
		checks := FindEnvServiceRefChecks(GetResourceCache(), "prod")
		if len(checks) != 0 {
			t.Errorf("expected no checks for valid split host+port, got %+v", checks)
		}
	})

	t.Run("host with embedded port is not double-suffixed by _PORT sibling", func(t *testing.T) {
		defer ResetTestState()
		replicas := int32(1)
		// FLAGD_HOST already carries :9090; a FLAGD_PORT sibling must NOT turn it
		// into flagd:9090:8013 (which parses as an invalid multi-colon host and
		// would silently drop the reference). The embedded port wins, so the
		// mismatch against the Service's 7000 surfaces on port 9090.
		client := fake.NewClientset(
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "frontend",
					Namespace:         "prod",
					CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: "app",
						Env: []corev1.EnvVar{
							{Name: "FLAGD_HOST", Value: "flagd:9090"},
							{Name: "FLAGD_PORT", Value: "8013"},
						},
					}}}},
				},
				Status: appsv1.DeploymentStatus{
					UnavailableReplicas: 1,
					Conditions:          []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse}},
				},
			},
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "flagd", Namespace: "prod"},
				Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 7000}}},
			},
		)
		if err := InitTestResourceCache(client); err != nil {
			t.Fatalf("InitTestResourceCache: %v", err)
		}
		c := waitForEnvServiceCheck(t, "prod", "port_mismatch")
		if c.EnvName != "FLAGD_HOST" {
			t.Errorf("EnvName = %q, want FLAGD_HOST", c.EnvName)
		}
		if c.ReferencedPort != 9090 {
			t.Errorf("ReferencedPort = %d, want 9090 (embedded port, not the _PORT sibling)", c.ReferencedPort)
		}
	})
}

func TestContainerPortIndex(t *testing.T) {
	envs := []corev1.EnvVar{
		{Name: "FLAGD_HOST", Value: "flagd"},
		{Name: "FLAGD_PORT", Value: "8013"},
		{Name: "OTEL_COLLECTOR_HOST", Value: "otel-collector"},
		{Name: "OTEL_COLLECTOR_PORT", Value: "4317"},
		{Name: "BAD_PORT", Value: "notanumber"},
		{Name: "ZERO_PORT", Value: "0"},
		{Name: "HIGH_PORT", Value: "99999"},
		{Name: "EMPTY_PORT", Value: ""},
	}
	idx := containerPortIndex(envs)
	cases := []struct {
		prefix string
		want   string
		found  bool
	}{
		{"FLAGD", "8013", true},
		{"OTEL_COLLECTOR", "4317", true},
		{"BAD", "", false},
		{"ZERO", "", false},
		{"HIGH", "", false},
		{"EMPTY", "", false},
	}
	for _, tc := range cases {
		got, ok := idx[tc.prefix]
		if ok != tc.found || got != tc.want {
			t.Errorf("containerPortIndex[%q] = (%q, %v), want (%q, %v)", tc.prefix, got, ok, tc.want, tc.found)
		}
	}
}

func TestDetectProblems_OperationalSignals(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	old := metav1.NewTime(now.Add(-10 * time.Minute))
	jobFailedAt := metav1.NewTime(now.Add(-2 * time.Minute))

	client := fake.NewClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "crashy", Namespace: "prod", CreationTimestamp: old},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:         "app",
					RestartCount: 3,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						Reason: "Error", ExitCode: 127,
						StartedAt: metav1.NewTime(now.Add(-3 * time.Second)), FinishedAt: metav1.NewTime(now.Add(-2 * time.Second)),
					}},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "not-ready", Namespace: "prod", Labels: map[string]string{"app": "not-ready"}, CreationTimestamp: old},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{{
					Type:   corev1.PodReady,
					Status: corev1.ConditionFalse,
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", Labels: map[string]string{"app": "api"}, CreationTimestamp: old},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:  "app",
				Ports: []corev1.ContainerPort{{Name: "admin", ContainerPort: 9090}},
			}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				}},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "missing"},
				Ports:    []corev1.ServicePort{{Port: 80}},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "not-ready", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "not-ready"},
				Ports:    []corev1.ServicePort{{Port: 80}},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "api"},
				Ports: []corev1.ServicePort{{
					Port:       80,
					TargetPort: intstr.FromString("http"),
				}},
			},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "prod", CreationTimestamp: old},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimLost},
		},
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "prod", CreationTimestamp: old},
			Status: batchv1.JobStatus{
				Conditions: []batchv1.JobCondition{{
					Type:               batchv1.JobFailed,
					Status:             corev1.ConditionTrue,
					Reason:             "BackoffLimitExceeded",
					Message:            "Job has reached the specified backoff limit",
					LastTransitionTime: jobFailedAt,
				}},
			},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "Pod", "crashy", "CrashLoopBackOff") &&
			hasProblem(problems, "Service", "empty", "Selector matches no pods") &&
			hasProblem(problems, "Service", "not-ready", "0/1 selected pods ready") &&
			hasProblem(problems, "Service", "api", "Unresolved named targetPort: http") &&
			hasProblem(problems, "PersistentVolumeClaim", "data", "Lost") &&
			hasProblem(problems, "Job", "migrate", "BackoffLimitExceeded") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Pod", "crashy", "CrashLoopBackOff", "critical")
	if got, ok := lookupProblem(problems, "Pod", "crashy", "CrashLoopBackOff"); !ok || !strings.Contains(got.Cause, "code 127") || !strings.Contains(got.Action, "command/args") {
		t.Fatalf("crashy diagnosis = %+v, want exit-code cause/action", got)
	}
	// "Selector matches no pods" is warning, not critical — could be a
	// deliberately scaled-to-zero workload. The "0/N selected pods ready"
	// case below stays critical (workload exists, routing is actually
	// broken).
	assertProblem(t, problems, "Service", "empty", "Selector matches no pods", "warning")
	assertProblem(t, problems, "Service", "not-ready", "0/1 selected pods ready", "critical")
	assertProblem(t, problems, "Service", "api", "Unresolved named targetPort: http", "high")
	assertProblem(t, problems, "PersistentVolumeClaim", "data", "Lost", "critical")
	assertProblem(t, problems, "Job", "migrate", "BackoffLimitExceeded", "critical")
}

func TestPodsMatchingServiceExcludesSucceededPods(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "prod"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "worker"}},
	}
	mkPod := func(name string, phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", Labels: map[string]string{"app": "worker"}},
			Status:     corev1.PodStatus{Phase: phase},
		}
	}

	// Succeeded (completed Job) pods are excluded; Failed pods are kept so a
	// Service backed only by failed pods still reads as a real outage.
	matched := podsMatchingService(svc, []*corev1.Pod{
		mkPod("done", corev1.PodSucceeded),
		mkPod("crashed", corev1.PodFailed),
		mkPod("serving", corev1.PodRunning),
	})
	gotNames := map[string]bool{}
	for _, p := range matched {
		gotNames[p.Name] = true
	}
	if len(matched) != 2 || !gotNames["serving"] || !gotNames["crashed"] {
		t.Fatalf("expected serving+crashed to match (Succeeded excluded), got %d: %+v", len(matched), matched)
	}

	// A Service backed solely by a completed Job pod must select no endpoints.
	if got := podsMatchingService(svc, []*corev1.Pod{mkPod("done", corev1.PodSucceeded)}); len(got) != 0 {
		t.Fatalf("Service matching only a completed Job pod should select no endpoints, got %d", len(got))
	}

	// ...but the selector DID match a completed pod, so the zero-endpoint message
	// can say so instead of the false "matches no pods".
	if !selectorMatchesSucceededPod(svc, []*corev1.Pod{mkPod("done", corev1.PodSucceeded)}) {
		t.Fatal("selectorMatchesSucceededPod should detect a matching completed pod")
	}
	if selectorMatchesSucceededPod(svc, []*corev1.Pod{mkPod("serving", corev1.PodRunning)}) {
		t.Fatal("selectorMatchesSucceededPod should be false when matches are not Succeeded")
	}
}

func TestImagePullDiagnosis(t *testing.T) {
	cases := []struct {
		name      string
		reason    string
		message   string
		wantCause string
		wantActs  []string
	}{
		{
			name:      "not found",
			reason:    "ImagePullBackOff",
			message:   `Back-off pulling image "reg.io/team/api:v2": failed to resolve reference "reg.io/team/api:v2": not found`,
			wantCause: "Image not found: reg.io/team/api:v2",
			wantActs:  []string{"repository and tag"},
		},
		{
			name:      "auth wins over not found",
			reason:    "ErrImagePull",
			message:   `failed to pull image "priv.io/app:v1": not found: authentication required`,
			wantCause: "Not authorized to pull image: priv.io/app:v1",
			wantActs:  []string{"imagePullSecrets", "repository/tag"},
		},
		{
			name:      "registry unreachable",
			reason:    "ImagePullBackOff",
			message:   `failed to pull image "reg.io/app:v1": dial tcp: lookup reg.io: no such host`,
			wantCause: "Registry unreachable: reg.io/app:v1",
			wantActs:  []string{"DNS"},
		},
		{
			name:      "rate limited",
			reason:    "ImagePullBackOff",
			message:   `toomanyrequests: rate limit exceeded for image "reg.io/app:v1"`,
			wantCause: "Registry rate-limited: reg.io/app:v1",
			wantActs:  []string{"authenticated"},
		},
		{
			name:      "invalid reference",
			reason:    "InvalidImageName",
			message:   `Failed to apply default image tag "bad image": invalid reference format`,
			wantCause: "Image reference is invalid",
			wantActs:  []string{"syntax"},
		},
		{
			name:    "unknown shape",
			reason:  "ImagePullBackOff",
			message: `some novel kubelet error for image "reg.io/app:v1"`,
		},
		{
			name:    "non image pull reason",
			reason:  "CrashLoopBackOff",
			message: `not found`,
		},
		{
			name:    "status code not embedded in tag",
			reason:  "ImagePullBackOff",
			message: `failed to pull image "reg.io/app:v401": unexpected response`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cause, action := imagePullDiagnosis(tc.reason, tc.message)
			if cause != tc.wantCause {
				t.Fatalf("cause = %q, want %q", cause, tc.wantCause)
			}
			if len(tc.wantActs) == 0 {
				if action != "" {
					t.Fatalf("action = %q, want empty", action)
				}
				return
			}
			for _, wantAct := range tc.wantActs {
				if !strings.Contains(action, wantAct) {
					t.Fatalf("action = %q, want substring %q", action, wantAct)
				}
			}
		})
	}
}

func TestDetectProblems_ImagePullDiagnosis(t *testing.T) {
	defer ResetTestState()

	old := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	client := fake.NewClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "prod", CreationTimestamp: old},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "ImagePullBackOff",
					Message: `failed to pull image "priv.io/app:v1": denied: requested access to the resource is denied`,
				}},
			}},
		},
	})

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "Pod", "private", "ImagePullBackOff") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	got, ok := lookupProblem(problems, "Pod", "private", "ImagePullBackOff")
	if !ok {
		t.Fatalf("missing image pull problem: %+v", problems)
	}
	if got.Cause != "Not authorized to pull image: priv.io/app:v1" || !strings.Contains(got.Action, "imagePullSecrets") {
		t.Fatalf("image pull diagnosis = %+v, want auth cause/action", got)
	}
	if !strings.Contains(got.Message, "requested access") {
		t.Fatalf("raw kubelet message should be preserved, got %q", got.Message)
	}
}

func TestDetectProblems_ProbeFailures(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	old := metav1.NewTime(now.Add(-10 * time.Minute))
	recent := metav1.NewTime(now.Add(-1 * time.Minute))
	timeless := metav1.Time{}

	client := fake.NewClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "readiness", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:           "app",
				ReadinessProbe: &corev1.Probe{},
			}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{{
					Type:               corev1.PodReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: old,
				}},
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "app",
					Ready: false,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: old}},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "liveness", Namespace: "prod", CreationTimestamp: old},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "app",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "thrash", Namespace: "prod", CreationTimestamp: old},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:         "app",
					Ready:        false,
					RestartCount: highRestartThreshold + 1,
					State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: old}},
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", FinishedAt: recent},
					},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "stale-probe", Namespace: "prod", CreationTimestamp: old},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "app",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				}},
			},
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "liveness.1", Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "prod", Name: "liveness"},
			Type:           corev1.EventTypeWarning,
			Reason:         "Unhealthy",
			Message:        "Liveness probe failed: HTTP probe failed with statuscode: 500",
			LastTimestamp:  recent,
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "thrash.1", Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "prod", Name: "thrash"},
			Type:           corev1.EventTypeWarning,
			Reason:         "Unhealthy",
			Message:        "Liveness probe failed: HTTP probe failed with statuscode: 500",
			LastTimestamp:  recent,
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "stale-probe.1", Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "prod", Name: "stale-probe"},
			Type:           corev1.EventTypeWarning,
			Reason:         "Unhealthy",
			Message:        "Liveness probe failed: HTTP probe failed with statuscode: 500",
			LastTimestamp:  timeless,
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "Pod", "readiness", "ReadinessProbeFailed") &&
			hasProblem(problems, "Pod", "liveness", "LivenessProbeFailed") &&
			hasProblem(problems, "Pod", "thrash", "HighRestartCount") &&
			hasProblem(problems, "Pod", "stale-probe", "CrashLoopBackOff") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Pod", "readiness", "ReadinessProbeFailed", "high")
	assertProblem(t, problems, "Pod", "liveness", "LivenessProbeFailed", "critical")
	assertProblem(t, problems, "Pod", "thrash", "HighRestartCount", "high")
	assertProblem(t, problems, "Pod", "stale-probe", "CrashLoopBackOff", "critical")
	if hasProblem(problems, "Pod", "thrash", "LivenessProbeFailed") {
		t.Fatalf("liveness event should not mask high restart thrash: %+v", problems)
	}
	if hasProblem(problems, "Pod", "stale-probe", "LivenessProbeFailed") {
		t.Fatalf("timeless probe event should not override the current pod reason: %+v", problems)
	}
	if got, ok := lookupProblem(problems, "Pod", "liveness", "LivenessProbeFailed"); !ok || !strings.Contains(got.Message, "HTTP probe failed") {
		t.Fatalf("liveness probe problem = %+v, want event message detail", got)
	}
}

func TestDetectProblems_InvalidProbeTargetAndStalledInit(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	old := metav1.NewTime(now.Add(-10 * time.Minute))
	recent := metav1.NewTime(now.Add(-1 * time.Minute))

	client := fake.NewClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-readiness", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:           "app",
				Ports:          []corev1.ContainerPort{{Name: "admin", ContainerPort: 9090}},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromString("http")}}},
			}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{{
					Type:               corev1.PodReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: recent,
				}},
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "app",
					Ready: false,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: old}},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-liveness", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:          "app",
				Ports:         []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
				LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString("admin")}}},
			}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "app",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "stuck-init", Namespace: "prod", CreationTimestamp: old},
			Spec: corev1.PodSpec{InitContainers: []corev1.Container{{
				Name:    "wait",
				Image:   "busybox",
				Command: []string{"sh", "-c", "while true; do sleep 5; done"},
			}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				InitContainerStatuses: []corev1.ContainerStatus{{
					Name:  "wait",
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: old}},
				}},
			},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "Pod", "bad-readiness", "ReadinessProbeInvalid") &&
			hasProblem(problems, "Pod", "bad-liveness", "LivenessProbeInvalid") &&
			hasProblem(problems, "Pod", "stuck-init", "InitContainerStalled") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Pod", "bad-readiness", "ReadinessProbeInvalid", "high")
	assertProblem(t, problems, "Pod", "bad-liveness", "LivenessProbeInvalid", "critical")
	assertProblem(t, problems, "Pod", "stuck-init", "InitContainerStalled", "high")
	if got, ok := lookupProblem(problems, "Pod", "bad-readiness", "ReadinessProbeInvalid"); !ok || !strings.Contains(got.Message, "named port \"http\"") {
		t.Fatalf("readiness invalid problem = %+v, want named port detail", got)
	}
	if got, ok := lookupProblem(problems, "Pod", "stuck-init", "InitContainerStalled"); !ok || !strings.Contains(got.Message, "init container \"wait\"") {
		t.Fatalf("stalled init problem = %+v, want init container detail", got)
	}
}

func TestDetectProblems_DaemonSetSchedulingStatus(t *testing.T) {
	defer ResetTestState()

	old := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	controller := true
	dsOwner := func(name string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: name, Controller: &controller}}
	}
	daemonSetTargetAffinity := func(nodeName string) *corev1.Affinity {
		if nodeName == "" {
			return nil
		}
		return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchFields: []corev1.NodeSelectorRequirement{{
						Key:      "metadata.name",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{nodeName},
					}},
				}},
			},
		}}
	}
	unschedulableDSPod := func(dsName, podName, targetNode, message string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: "prod", CreationTimestamp: old, OwnerReferences: dsOwner(dsName)},
			Spec:       corev1.PodSpec{Affinity: daemonSetTargetAffinity(targetNode)},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				Conditions: []corev1.PodCondition{{
					Type:    corev1.PodScheduled,
					Status:  corev1.ConditionFalse,
					Reason:  corev1.PodReasonUnschedulable,
					Message: message,
				}},
			},
		}
	}
	assignedUnreadyDSPod := func(dsName, podName, nodeName string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: "prod", CreationTimestamp: old, OwnerReferences: dsOwner(dsName)},
			Spec: corev1.PodSpec{
				NodeName:   nodeName,
				Containers: []corev1.Container{{Name: "main", Image: "nginx"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.ContainersReady, Status: corev1.ConditionFalse},
					{Type: corev1.PodReady, Status: corev1.ConditionFalse},
				},
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "main",
					Ready: false,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				}},
			},
		}
	}
	client := fake.NewClientset(
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 2,
				NumberUnavailable:      2,
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "wrong-node", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 4,
				NumberMisscheduled:     1,
				NumberUnavailable:      1,
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "design-taint", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 4,
				NumberUnavailable:      1,
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "design-affinity", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 4,
				NumberUnavailable:      1,
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "capacity", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 4,
				NumberUnavailable:      1,
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "host-port", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 4,
				NumberUnavailable:      1,
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "lifecycle-taint", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 4,
				NumberUnavailable:      1,
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "unparsed", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 4,
				NumberUnavailable:      1,
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "not-scheduled-design", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 3,
				NumberUnavailable:      1,
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "partial-duplicate", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 4,
				NumberUnavailable:      2,
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "extra-unknown-target", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 4,
				NumberUnavailable:      1,
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "mixed-unready", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 4,
				CurrentNumberScheduled: 4,
				NumberUnavailable:      1,
			},
		},
		unschedulableDSPod("design-taint", "design-taint-pod", "node-a", "0/4 nodes are available: 4 node(s) had untolerated taint {dedicated: infra}."),
		unschedulableDSPod("design-affinity", "design-affinity-pod", "node-b", "0/4 nodes are available: 4 node(s) didn't match Pod's node affinity/selector."),
		unschedulableDSPod("capacity", "capacity-pod", "node-c", "0/4 nodes are available: 4 Insufficient cpu."),
		unschedulableDSPod("host-port", "host-port-pod", "node-d", "0/4 nodes are available: 4 node(s) didn't have free ports for the requested pod ports."),
		unschedulableDSPod("lifecycle-taint", "lifecycle-taint-pod", "node-l", "0/4 nodes are available: 4 node(s) had untolerated taint {node.kubernetes.io/not-ready: }."),
		unschedulableDSPod("unparsed", "unparsed-pod", "node-u", "0/4 nodes are available: 4 future scheduler predicate we do not parse yet."),
		unschedulableDSPod("not-scheduled-design", "not-scheduled-design-pod", "node-e", "0/4 nodes are available: 4 node(s) had untolerated taint {dedicated: infra}."),
		unschedulableDSPod("partial-duplicate", "partial-duplicate-pod-a", "node-f", "0/4 nodes are available: 4 node(s) had untolerated taint {dedicated: infra}."),
		unschedulableDSPod("partial-duplicate", "partial-duplicate-pod-b", "node-f", "0/4 nodes are available: 4 node(s) didn't match Pod's node affinity/selector."),
		unschedulableDSPod("extra-unknown-target", "extra-unknown-target-known", "node-g", "0/4 nodes are available: 4 node(s) had untolerated taint {dedicated: infra}."),
		unschedulableDSPod("extra-unknown-target", "extra-unknown-target-empty", "", "0/4 nodes are available: 4 node(s) didn't match Pod's node affinity/selector."),
		unschedulableDSPod("mixed-unready", "mixed-unready-placement-pod", "node-h", "0/4 nodes are available: 4 node(s) had untolerated taint {dedicated: infra}."),
		assignedUnreadyDSPod("mixed-unready", "mixed-unready-crashing-pod", "node-i"),
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "DaemonSet", "missing", "2 not scheduled") &&
			hasProblem(problems, "DaemonSet", "wrong-node", "1 misscheduled") &&
			hasProblem(problems, "DaemonSet", "wrong-node", "1 unavailable") &&
			hasProblem(problems, "DaemonSet", "design-taint", "1 unavailable") &&
			hasProblem(problems, "DaemonSet", "design-affinity", "1 unavailable") &&
			hasProblem(problems, "DaemonSet", "capacity", "1 unavailable") &&
			hasProblem(problems, "DaemonSet", "host-port", "1 unavailable") &&
			hasProblem(problems, "DaemonSet", "lifecycle-taint", "1 unavailable") &&
			hasProblem(problems, "DaemonSet", "unparsed", "1 unavailable") &&
			hasProblem(problems, "DaemonSet", "not-scheduled-design", "1 not scheduled") &&
			hasProblem(problems, "DaemonSet", "partial-duplicate", "2 unavailable") &&
			hasProblem(problems, "DaemonSet", "extra-unknown-target", "1 unavailable") &&
			hasProblem(problems, "DaemonSet", "mixed-unready", "1 unavailable") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "DaemonSet", "missing", "2 not scheduled", "critical")
	assertProblem(t, problems, "DaemonSet", "wrong-node", "1 misscheduled", "high")
	assertProblem(t, problems, "DaemonSet", "wrong-node", "1 unavailable", "critical")
	assertProblem(t, problems, "DaemonSet", "design-taint", "1 unavailable", "high")
	assertProblem(t, problems, "DaemonSet", "design-affinity", "1 unavailable", "high")
	assertProblem(t, problems, "DaemonSet", "capacity", "1 unavailable", "critical")
	assertProblem(t, problems, "DaemonSet", "host-port", "1 unavailable", "critical")
	assertProblem(t, problems, "DaemonSet", "lifecycle-taint", "1 unavailable", "critical")
	assertProblem(t, problems, "DaemonSet", "unparsed", "1 unavailable", "critical")
	assertProblem(t, problems, "DaemonSet", "not-scheduled-design", "1 not scheduled", "critical")
	assertProblem(t, problems, "DaemonSet", "partial-duplicate", "2 unavailable", "critical")
	assertProblem(t, problems, "DaemonSet", "extra-unknown-target", "1 unavailable", "high")
	assertProblem(t, problems, "DaemonSet", "mixed-unready", "1 unavailable", "critical")
}

func TestDetectProblems_DeploymentReplicaFailure(t *testing.T) {
	defer ResetTestState()

	old := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	client := fake.NewClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", CreationTimestamp: old},
			Status: appsv1.DeploymentStatus{
				Conditions: []appsv1.DeploymentCondition{{
					Type:               appsv1.DeploymentReplicaFailure,
					Status:             corev1.ConditionTrue,
					Reason:             "FailedCreate",
					Message:            "pods is forbidden: exceeded quota",
					LastTransitionTime: old,
				}, {
					Type:               appsv1.DeploymentProgressing,
					Status:             corev1.ConditionFalse,
					Reason:             "ProgressDeadlineExceeded",
					Message:            "ReplicaSet has timed out progressing",
					LastTransitionTime: old,
				}},
			},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "Deployment", "api", "ReplicaFailure") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Deployment", "api", "ReplicaFailure", "critical")
	if hasProblem(problems, "Deployment", "api", "Rollout stuck") {
		t.Fatalf("ReplicaFailure should suppress duplicate rollout-stuck row for the same Deployment: %+v", problems)
	}
	if p, ok := lookupProblem(problems, "Deployment", "api", "ReplicaFailure"); !ok || !strings.Contains(p.Message, "exceeded quota") {
		t.Fatalf("replica failure problem = %+v, want controller message", p)
	}
}

func TestDetectProblems_NetworkAndStorageState(t *testing.T) {
	defer ResetTestState()

	old := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	client := fake.NewClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "edge", Namespace: "prod", CreationTimestamp: old},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "assigned", Namespace: "prod", CreationTimestamp: old},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
			Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{
				IP: "203.0.113.10",
			}}}},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "grow", Namespace: "prod", CreationTimestamp: old},
			Status: corev1.PersistentVolumeClaimStatus{
				Phase:    corev1.ClaimBound,
				Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				Conditions: []corev1.PersistentVolumeClaimCondition{{
					Type:               corev1.PersistentVolumeClaimConditionType("ControllerResizeError"),
					Status:             corev1.ConditionTrue,
					Message:            "resize rejected by storage backend",
					LastTransitionTime: old,
				}},
			},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "fs-pending", Namespace: "prod", CreationTimestamp: old},
			Status: corev1.PersistentVolumeClaimStatus{
				Phase: corev1.ClaimBound,
				Conditions: []corev1.PersistentVolumeClaimCondition{{
					Type:               corev1.PersistentVolumeClaimFileSystemResizePending,
					Status:             corev1.ConditionTrue,
					Message:            "waiting for filesystem expansion on node",
					LastTransitionTime: old,
				}},
			},
		},
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-bad", CreationTimestamp: old},
			Status: corev1.PersistentVolumeStatus{
				Phase:   corev1.VolumeFailed,
				Message: "volume is gone",
			},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "")
		if hasProblem(problems, "Service", "edge", "LoadBalancer pending") &&
			hasProblem(problems, "PersistentVolumeClaim", "grow", "ControllerResizeError") &&
			hasProblem(problems, "PersistentVolume", "pv-bad", "Failed") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Service", "edge", "LoadBalancer pending", "high")
	if hasProblem(problems, "Service", "assigned", "LoadBalancer pending") {
		t.Fatalf("assigned LoadBalancer Service should not be flagged: %+v", problems)
	}
	assertProblem(t, problems, "PersistentVolumeClaim", "grow", "ControllerResizeError", "critical")
	if hasProblem(problems, "PersistentVolumeClaim", "fs-pending", string(corev1.PersistentVolumeClaimFileSystemResizePending)) {
		t.Fatalf("FileSystemResizePending is in-progress, not a resize failure: %+v", problems)
	}
	assertProblem(t, problems, "PersistentVolume", "pv-bad", "Failed", "critical")
}

func TestDetectProblems_PVCPendingWarningEventDiagnosis(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	old := metav1.NewTime(now.Add(-10 * time.Minute))
	recent := metav1.NewTime(now.Add(-1 * time.Minute))
	client := fake.NewClientset(
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "slow", Namespace: "prod", CreationTimestamp: old},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "normal-only", Namespace: "prod", CreationTimestamp: old},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "slow.1", Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "PersistentVolumeClaim", Namespace: "prod", Name: "slow"},
			Type:           corev1.EventTypeWarning,
			Reason:         "ProvisioningFailed",
			Message:        "failed to provision volume with StorageClass fast: rpc error: quota exceeded",
			LastTimestamp:  recent,
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "normal-only.1", Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "PersistentVolumeClaim", Namespace: "prod", Name: "normal-only"},
			Type:           corev1.EventTypeNormal,
			Reason:         "ExternalProvisioning",
			Message:        "waiting for a volume to be created by the external provisioner",
			LastTimestamp:  recent,
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "PersistentVolumeClaim", "slow", "Pending") &&
			hasProblem(problems, "PersistentVolumeClaim", "normal-only", "Pending") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	slow, ok := lookupProblem(problems, "PersistentVolumeClaim", "slow", "Pending")
	if !ok {
		t.Fatalf("missing slow PVC problem: %+v", problems)
	}
	if slow.Cause != "Storage provisioner failed to create a volume." || !strings.Contains(slow.Action, "CSI controller") {
		t.Fatalf("slow PVC diagnosis = %+v, want provisioner cause/action", slow)
	}
	if !strings.Contains(slow.Message, "quota exceeded") {
		t.Fatalf("slow PVC message = %q, want raw event detail", slow.Message)
	}

	normal, ok := lookupProblem(problems, "PersistentVolumeClaim", "normal-only", "Pending")
	if !ok {
		t.Fatalf("missing normal-only PVC problem: %+v", problems)
	}
	if normal.Cause != "" || normal.Action != "" || normal.Message != "PVC is unbound — no volume has been provisioned" {
		t.Fatalf("normal ExternalProvisioning event should not diagnose PVC, got %+v", normal)
	}
}

func TestDetectProblems_PVCPendingWaitForFirstConsumerStillSuppressed(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	old := metav1.NewTime(now.Add(-10 * time.Minute))
	mode := storagev1.VolumeBindingWaitForFirstConsumer
	scName := "late-bind"
	storageClass := &storagev1.StorageClass{
		ObjectMeta:        metav1.ObjectMeta{Name: "late-bind"},
		VolumeBindingMode: &mode,
	}
	client := fake.NewClientset(
		storageClass,
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "awaiting-consumer", Namespace: "prod", CreationTimestamp: old},
			Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: &scName},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "awaiting-consumer.1", Namespace: "prod"},
			InvolvedObject: corev1.ObjectReference{Kind: "PersistentVolumeClaim", Namespace: "prod", Name: "awaiting-consumer"},
			Type:           corev1.EventTypeWarning,
			Reason:         "FailedBinding",
			Message:        "waiting for first consumer to be created before binding",
			LastTimestamp:  metav1.NewTime(now.Add(-1 * time.Minute)),
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	time.Sleep(50 * time.Millisecond)
	if problems := DetectProblems(cache, "prod"); hasProblem(problems, "PersistentVolumeClaim", "awaiting-consumer", "Pending") {
		t.Fatalf("WaitForFirstConsumer PVC should stay suppressed despite Warning event: %+v", problems)
	}
}

func TestDetectProblems_TerminatingResources(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	oldCreated := metav1.NewTime(now.Add(-2 * time.Hour))
	oldDelete := metav1.NewTime(now.Add(-35 * time.Minute))
	recentDelete := metav1.NewTime(now.Add(-2 * time.Minute))

	client := fake.NewClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pod-stuck",
				Namespace:         "prod",
				CreationTimestamp: oldCreated,
				DeletionTimestamp: &oldDelete,
				Finalizers:        []string{"example.com/finalizer"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "svc-recent",
				Namespace:         "prod",
				CreationTimestamp: oldCreated,
				DeletionTimestamp: &recentDelete,
			},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "deploy-stuck",
				Namespace:         "prod",
				CreationTimestamp: oldCreated,
				DeletionTimestamp: &oldDelete,
			},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "Pod", "pod-stuck", "Terminating stuck") &&
			hasProblem(problems, "Deployment", "deploy-stuck", "Terminating stuck") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Pod", "pod-stuck", "Terminating stuck", "critical")
	if p, ok := lookupProblem(problems, "Pod", "pod-stuck", "Terminating stuck"); !ok || !strings.Contains(p.Message, "example.com/finalizer") {
		t.Fatalf("terminating pod problem = %+v, want finalizer context", p)
	}
	assertProblem(t, problems, "Deployment", "deploy-stuck", "Terminating stuck", "critical")
	if hasProblem(problems, "Service", "svc-recent", "Terminating stuck") {
		t.Fatalf("recently deleting Service should not be flagged: %+v", problems)
	}
}

func TestDetectProblems_TerminatingNamespaceClusterScoped(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	oldCreated := metav1.NewTime(now.Add(-2 * time.Hour))
	oldDelete := metav1.NewTime(now.Add(-35 * time.Minute))

	client := fake.NewClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "stuck",
			CreationTimestamp: oldCreated,
			DeletionTimestamp: &oldDelete,
			Finalizers:        []string{"kubernetes"},
		},
		Status: corev1.NamespaceStatus{
			Phase: corev1.NamespaceTerminating,
			Conditions: []corev1.NamespaceCondition{{
				Type:    corev1.NamespaceFinalizersRemaining,
				Status:  corev1.ConditionTrue,
				Reason:  "SomeFinalizersRemain",
				Message: "example.com/finalizer remains",
			}},
		},
	})

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "")
		if hasProblem(problems, "Namespace", "stuck", "Namespace terminating stuck") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assertProblem(t, problems, "Namespace", "stuck", "Namespace terminating stuck", "critical")
	if p, ok := lookupProblem(problems, "Namespace", "stuck", "Namespace terminating stuck"); !ok || !strings.Contains(p.Message, "NamespaceFinalizersRemaining") {
		t.Fatalf("terminating namespace problem = %+v, want status condition context", p)
	}
	if scoped := DetectProblems(cache, "prod"); hasProblem(scoped, "Namespace", "stuck", "Namespace terminating stuck") {
		t.Fatalf("namespace-scoped scan should not include cluster-scoped namespace issue: %+v", scoped)
	}
}

func hasProblem(problems []Detection, kind, name, reason string) bool {
	for _, p := range problems {
		if p.Kind == kind && p.Name == name && p.Reason == reason {
			return true
		}
	}
	return false
}

func assertProblem(t *testing.T, problems []Detection, kind, name, reason, severity string) {
	t.Helper()
	for _, p := range problems {
		if p.Kind != kind || p.Name != name || p.Reason != reason {
			continue
		}
		if p.Severity != severity {
			t.Fatalf("%s/%s severity = %q, want %q; problem=%+v", kind, name, p.Severity, severity, p)
		}
		return
	}
	t.Fatalf("missing problem kind=%s name=%s reason=%q; got %+v", kind, name, reason, problems)
}

func lookupProblem(problems []Detection, kind, name, reason string) (Detection, bool) {
	for _, p := range problems {
		if p.Kind == kind && p.Name == name && p.Reason == reason {
			return p, true
		}
	}
	return Detection{}, false
}

func TestDetectProblems_PDBBlocksEvictions(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	old := metav1.NewTime(now.Add(-10 * time.Minute))
	one := intstr.FromInt32(1)
	half := intstr.FromString("50%")

	mkPDB := func(name string, minAvailable intstr.IntOrString, allowed, current, desired, expected int32) *policyv1.PodDisruptionBudget {
		return &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", CreationTimestamp: old, Generation: 1},
			Spec: policyv1.PodDisruptionBudgetSpec{
				MinAvailable: &minAvailable,
				Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			},
			Status: policyv1.PodDisruptionBudgetStatus{
				ObservedGeneration: 1,
				DisruptionsAllowed: allowed,
				CurrentHealthy:     current,
				DesiredHealthy:     desired,
				ExpectedPods:       expected,
				Conditions: []metav1.Condition{{
					Type:               policyv1.DisruptionAllowedCondition,
					Status:             metav1.ConditionFalse,
					Reason:             policyv1.InsufficientPodsReason,
					LastTransitionTime: old,
				}},
			},
		}
	}

	client := fake.NewClientset(
		mkPDB("blocked", one, 0, 1, 1, 1),                // all selected pods healthy, but no eviction budget
		mkPDB("temporarily-unhealthy", half, 0, 1, 1, 2), // no budget because a pod is unhealthy
		mkPDB("has-budget", half, 1, 3, 2, 3),            // healthy and at least one eviction allowed
		mkPDB("empty", one, 0, 0, 0, 0),                  // selector currently matches no pods
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	const reason = "Voluntary evictions blocked"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasProblem(DetectProblems(cache, "prod"), "PodDisruptionBudget", "blocked", reason) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	problems := DetectProblems(cache, "prod")

	p, ok := lookupProblem(problems, "PodDisruptionBudget", "blocked", reason)
	if !ok {
		t.Fatalf("missing blocked PDB problem; got %+v", problems)
	}
	if p.Severity != "high" || p.Group != "policy" {
		t.Fatalf("blocked PDB severity/group = %q/%q, want high/policy; problem=%+v", p.Severity, p.Group, p)
	}
	if !strings.Contains(p.Message, "node drains and upgrades cannot evict") {
		t.Fatalf("blocked PDB message should explain drain/upgrade impact; got %q", p.Message)
	}
	for _, name := range []string{"temporarily-unhealthy", "has-budget", "empty"} {
		if hasProblem(problems, "PodDisruptionBudget", name, reason) {
			t.Errorf("PDB %s should not be flagged as structurally blocking evictions: %+v", name, problems)
		}
	}
}

// TestDetectProblems_SharedRWOVolume pins the multi-replica ReadWriteOnce
// conflict detector: a Deployment wanting >1 replica that mounts an RWO PVC is
// flagged (only one node can attach it), while a single-replica RWO mount and a
// multi-replica ReadWriteMany mount are not.
func TestDetectProblems_SharedRWOVolume(t *testing.T) {
	defer ResetTestState()

	two := int32(2)
	one := int32(1)
	three := int32(3)

	mkDeploy := func(name string, replicas *int32, claim string) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod"},
			Spec: appsv1.DeploymentSpec{
				Replicas: replicas,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:         "app",
						VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
					}},
					Volumes: []corev1.Volume{{
						Name:         "data",
						VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}},
					}},
				}},
			},
		}
	}
	mkPVC := func(name string, mode corev1.PersistentVolumeAccessMode) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod"},
			Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{mode}},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, AccessModes: []corev1.PersistentVolumeAccessMode{mode}},
		}
	}

	client := fake.NewClientset(
		mkDeploy("conflict", &two, "rwo-pvc"), // 2 replicas + RWO → flagged
		mkDeploy("single", &one, "rwo-pvc"),   // 1 replica + RWO → fine
		mkDeploy("rwx", &three, "rwx-pvc"),    // 3 replicas + RWX → fine
		mkPVC("rwo-pvc", corev1.ReadWriteOnce),
		mkPVC("rwx-pvc", corev1.ReadWriteMany),
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	const reason = "ReadWriteOnce volume shared across replicas"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasProblem(DetectProblems(cache, "prod"), "Deployment", "conflict", reason) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	problems := DetectProblems(cache, "prod")

	assertProblem(t, problems, "Deployment", "conflict", reason, "high")
	if hasProblem(problems, "Deployment", "single", reason) {
		t.Errorf("single-replica RWO mount should not be flagged: %+v", problems)
	}
	if hasProblem(problems, "Deployment", "rwx", reason) {
		t.Errorf("multi-replica RWX mount should not be flagged: %+v", problems)
	}
}

func TestDetectProblems_RolloutStuckExplainsRWORollingUpdate(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	old := metav1.NewTime(now.Add(-20 * time.Minute))
	transition := metav1.NewTime(now.Add(-5 * time.Minute))
	one := int32(1)

	mkDeploy := func(name string, strategy appsv1.DeploymentStrategyType) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", CreationTimestamp: old},
			Spec: appsv1.DeploymentSpec{
				Replicas: &one,
				Strategy: appsv1.DeploymentStrategy{Type: strategy},
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:         "app",
						VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
					}},
					Volumes: []corev1.Volume{{
						Name:         "data",
						VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}},
					}},
				}},
			},
			Status: appsv1.DeploymentStatus{
				Conditions: []appsv1.DeploymentCondition{{
					Type:               appsv1.DeploymentProgressing,
					Status:             corev1.ConditionFalse,
					Reason:             "ProgressDeadlineExceeded",
					Message:            "ReplicaSet has timed out progressing.",
					LastTransitionTime: transition,
				}},
			},
		}
	}
	client := fake.NewClientset(
		mkDeploy("rolling", appsv1.RollingUpdateDeploymentStrategyType),
		mkDeploy("recreate", appsv1.RecreateDeploymentStrategyType),
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "prod"},
			Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}},
		},
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	const reason = "Rollout stuck"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasProblem(DetectProblems(cache, "prod"), "Deployment", "rolling", reason) &&
			hasProblem(DetectProblems(cache, "prod"), "Deployment", "recreate", reason) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	problems := DetectProblems(cache, "prod")

	rolling, ok := lookupProblem(problems, "Deployment", "rolling", reason)
	if !ok {
		t.Fatalf("missing rolling rollout problem; got %+v", problems)
	}
	if !strings.Contains(rolling.Message, "strategy: Recreate") || !strings.Contains(rolling.Message, `ReadWriteOnce PVC "data"`) {
		t.Fatalf("rolling rollout message should include RWO/RollingUpdate fix; got %q", rolling.Message)
	}
	recreate, ok := lookupProblem(problems, "Deployment", "recreate", reason)
	if !ok {
		t.Fatalf("missing recreate rollout problem; got %+v", problems)
	}
	if strings.Contains(recreate.Message, "strategy: Recreate") {
		t.Fatalf("recreate rollout should not get RWO/RollingUpdate hint; got %q", recreate.Message)
	}
}

// Pins the observedGeneration==1 issue_timing rule for stuck rollouts: gen 1 rescues
// only the no-verdict (gray zone / missing LTT) case. A timestamp-backed
// "started_after_resource_was_healthy" verdict must survive — generation only bumps on spec changes, so
// a gen-1 Deployment that ran healthy then broke is still gen 1.
func TestDetectProblems_RolloutStuckIssueTimingGen1(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	stuckCond := func(ltt time.Time) []appsv1.DeploymentCondition {
		return []appsv1.DeploymentCondition{{
			Type:               appsv1.DeploymentProgressing,
			Status:             corev1.ConditionFalse,
			Reason:             "ProgressDeadlineExceeded",
			Message:            "ReplicaSet has timed out progressing",
			LastTransitionTime: metav1.NewTime(ltt),
		}}
	}
	client := fake.NewClientset(
		// Healthy ~2h, then stuck 10m ago → started_after_resource_was_healthy; gen==1 must not override.
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "gen1-runtime", Namespace: "prod", CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour))},
			Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, Conditions: stuckCond(now.Add(-10 * time.Minute))},
		},
		// Gray zone (healthy ~4m of an 8m life) → no LTT verdict → gen==1 rescue.
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "gen1-gray", Namespace: "prod", CreationTimestamp: metav1.NewTime(now.Add(-8 * time.Minute))},
			Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, Conditions: stuckCond(now.Add(-4 * time.Minute))},
		},
		// Never healthy: Available=False pinned at creation outranks the
		// recent Progressing LTT (which re-triggers on every rollout retry and
		// would otherwise read as a months-long healthy window).
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "never-healthy", Namespace: "prod", CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour))},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 5, Conditions: append(stuckCond(now.Add(-10*time.Minute)), appsv1.DeploymentCondition{
				Type:               appsv1.DeploymentAvailable,
				Status:             corev1.ConditionFalse,
				Reason:             "MinimumReplicasUnavailable",
				LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Hour)),
			})},
		},
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "Deployment", "gen1-runtime", "Rollout stuck") && hasProblem(problems, "Deployment", "gen1-gray", "Rollout stuck") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	rt, ok := lookupProblem(problems, "Deployment", "gen1-runtime", "Rollout stuck")
	if !ok || rt.IssueTiming != "started_after_resource_was_healthy" || rt.IssueTimingBasis != "condition" {
		t.Errorf("gen1-runtime issue_timing = (%q, %q), want (started_after_resource_was_healthy, condition); ok=%v", rt.IssueTiming, rt.IssueTimingBasis, ok)
	}
	gray, ok := lookupProblem(problems, "Deployment", "gen1-gray", "Rollout stuck")
	if !ok || gray.IssueTiming != "started_at_resource_creation" || gray.IssueTimingBasis != "spec" {
		t.Errorf("gen1-gray issue_timing = (%q, %q), want (started_at_resource_creation, spec); ok=%v", gray.IssueTiming, gray.IssueTimingBasis, ok)
	}
	nh, ok := lookupProblem(problems, "Deployment", "never-healthy", "Rollout stuck")
	if !ok || nh.IssueTiming != "started_at_resource_creation" || nh.IssueTimingBasis != "condition" {
		t.Errorf("never-healthy issue_timing = (%q, %q), want (started_at_resource_creation, condition); ok=%v", nh.IssueTiming, nh.IssueTimingBasis, ok)
	}
}

// Pins the two pod-issue_timing paths: a crashloop pod created alongside a young
// Deployment classifies started_at_resource_creation via creation-timestamp proximity
// (pod_creation basis — the Available condition races with CrashLoopBackOff's
// brief ready windows), while the same shape on an old Deployment with no
// Available=False condition must omit issue_timing rather than guess.
func TestDetectProblems_PodIssueTimingCreationProximity(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	controller := true
	crashloopPodR := func(name, rsName string, created time.Time, restarts int32) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "prod", CreationTimestamp: metav1.NewTime(created),
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rsName, Controller: &controller}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:         "app",
					RestartCount: restarts,
					State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				}},
			},
		}
	}
	crashloopPod := func(name, rsName string, created time.Time) *corev1.Pod {
		return crashloopPodR(name, rsName, created, 0)
	}
	rs := func(name, depName string, created time.Time) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "prod", CreationTimestamp: metav1.NewTime(created),
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: depName, Controller: &controller}},
		}}
	}
	deployWithAvail := func(name string, created time.Time, avail corev1.ConditionStatus, availLTT time.Time) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", CreationTimestamp: metav1.NewTime(created)},
			Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentAvailable, Status: avail, LastTransitionTime: metav1.NewTime(availLTT),
			}}},
		}
	}

	youngCreated := now.Add(-5 * time.Minute)
	veteranCreated := now.Add(-2 * time.Hour)
	client := fake.NewClientset(
		// Young Deployment, pod created 40s after it → pod_creation started_at_resource_creation.
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "young", Namespace: "prod", CreationTimestamp: metav1.NewTime(youngCreated)}},
		rs("young-1", "young", youngCreated),
		crashloopPod("young-1-abc", "young-1", youngCreated.Add(40*time.Second)),
		// Old Deployment, original pod crashing now, no Available=False → omit.
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "veteran", Namespace: "prod", CreationTimestamp: metav1.NewTime(veteranCreated)}},
		rs("veteran-1", "veteran", veteranCreated),
		crashloopPod("veteran-1-abc", "veteran-1", veteranCreated.Add(30*time.Second)),
		// Backoff replacement: pod recreated 4m after the dep, but in the
		// ORIGINAL RS (created with the dep) with restarts → backdate to deploy.
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "replaced", Namespace: "prod", CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute))}},
		rs("replaced-1", "replaced", now.Add(-10*time.Minute)),
		crashloopPodR("replaced-1-abc", "replaced-1", now.Add(-6*time.Minute), 4),
		// Mid-life rollout regression: NEW RS created 2m ago on a 10m-old dep;
		// its crashing pod must NOT be backdated to deploy time.
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "midroll", Namespace: "prod", CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute))}},
		rs("midroll-1", "midroll", now.Add(-10*time.Minute)),
		rs("midroll-2", "midroll", now.Add(-2*time.Minute)),
		crashloopPodR("midroll-2-abc", "midroll-2", now.Add(-90*time.Second), 3),
		// Chronic flapper: 2h-old deployment whose Available LTT keeps
		// resetting as the crashloop cycles readiness. The flap-poisoned LTT
		// must not produce after-healthy — omit.
		deployWithAvail("flapper", now.Add(-2*time.Hour), corev1.ConditionFalse, now.Add(-5*time.Minute)),
		rs("flapper-1", "flapper", now.Add(-2*time.Hour)),
		crashloopPodR("flapper-1-abc", "flapper-1", now.Add(-30*time.Minute), 22),
		// Never healthy: Available=False pinned at creation never went True —
		// flap-immune proof of at-creation even for an old crashlooper.
		deployWithAvail("nevergood", now.Add(-2*time.Hour), corev1.ConditionFalse, now.Add(-2*time.Hour)),
		rs("nevergood-1", "nevergood", now.Add(-2*time.Hour)),
		crashloopPodR("nevergood-1-abc", "nevergood-1", now.Add(-40*time.Minute), 22),
		// Surge-rollout regression: new RS 20m ago while Available is still
		// True (old pods serving) — the workload was demonstrably healthy
		// before this rollout.
		deployWithAvail("surge", now.Add(-2*time.Hour), corev1.ConditionTrue, now.Add(-2*time.Hour)),
		rs("surge-1", "surge", now.Add(-2*time.Hour)),
		rs("surge-2", "surge", now.Add(-20*time.Minute)),
		crashloopPodR("surge-2-abc", "surge-2", now.Add(-18*time.Minute), 5),
		// Stable-pod late failure: ImagePull-style pods never start, so
		// readiness never cycles and the Available LTT is trustworthy.
		deployWithAvail("latebreak", now.Add(-2*time.Hour), corev1.ConditionFalse, now.Add(-30*time.Minute)),
		rs("latebreak-1", "latebreak", now.Add(-2*time.Hour)),
		crashloopPodR("latebreak-1-abc", "latebreak-1", now.Add(-25*time.Minute), 0),
	)

	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	deadline := time.Now().Add(2 * time.Second)
	var problems []Detection
	for time.Now().Before(deadline) {
		problems = DetectProblems(cache, "prod")
		if hasProblem(problems, "Pod", "young-1-abc", "CrashLoopBackOff") && hasProblem(problems, "Pod", "veteran-1-abc", "CrashLoopBackOff") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	young, ok := lookupProblem(problems, "Pod", "young-1-abc", "CrashLoopBackOff")
	if !ok || young.IssueTiming != "started_at_resource_creation" || young.IssueTimingBasis != "pod_creation" {
		t.Errorf("young pod issue_timing = (%q, %q), want (started_at_resource_creation, pod_creation); ok=%v", young.IssueTiming, young.IssueTimingBasis, ok)
	}
	veteran, ok := lookupProblem(problems, "Pod", "veteran-1-abc", "CrashLoopBackOff")
	if !ok || veteran.IssueTiming != "" || veteran.IssueTimingBasis != "" {
		t.Errorf("veteran pod issue_timing = (%q, %q), want omitted; ok=%v", veteran.IssueTiming, veteran.IssueTimingBasis, ok)
	}
	replaced, ok := lookupProblem(problems, "Pod", "replaced-1-abc", "CrashLoopBackOff")
	if !ok || replaced.IssueTiming != "started_at_resource_creation" || replaced.IssueTimingBasis != "pod_creation" {
		t.Errorf("backoff-replacement pod issue_timing = (%q, %q), want (started_at_resource_creation, pod_creation); ok=%v", replaced.IssueTiming, replaced.IssueTimingBasis, ok)
	}
	midroll, ok := lookupProblem(problems, "Pod", "midroll-2-abc", "CrashLoopBackOff")
	if !ok || midroll.IssueTiming == "started_at_resource_creation" {
		t.Errorf("mid-rollout pod in a new RS must not be backdated to deploy time, got (%q, %q); ok=%v", midroll.IssueTiming, midroll.IssueTimingBasis, ok)
	}
	flapper, ok := lookupProblem(problems, "Pod", "flapper-1-abc", "CrashLoopBackOff")
	if !ok || flapper.IssueTiming != "" {
		t.Errorf("chronic flapper must omit issue_timing (flap-poisoned Available LTT), got (%q, %q); ok=%v", flapper.IssueTiming, flapper.IssueTimingBasis, ok)
	}
	nevergood, ok := lookupProblem(problems, "Pod", "nevergood-1-abc", "CrashLoopBackOff")
	if !ok || nevergood.IssueTiming != "started_at_resource_creation" || nevergood.IssueTimingBasis != "owner_condition" {
		t.Errorf("never-healthy crashlooper = (%q, %q), want (started_at_resource_creation, owner_condition); ok=%v", nevergood.IssueTiming, nevergood.IssueTimingBasis, ok)
	}
	surge, ok := lookupProblem(problems, "Pod", "surge-2-abc", "CrashLoopBackOff")
	if !ok || surge.IssueTiming != "started_after_resource_was_healthy" || surge.IssueTimingBasis != "pod_creation" {
		t.Errorf("surge-rollout pod = (%q, %q), want (started_after_resource_was_healthy, pod_creation); ok=%v", surge.IssueTiming, surge.IssueTimingBasis, ok)
	}
	latebreak, ok := lookupProblem(problems, "Pod", "latebreak-1-abc", "CrashLoopBackOff")
	if !ok || latebreak.IssueTiming != "started_after_resource_was_healthy" || latebreak.IssueTimingBasis != "owner_condition" {
		t.Errorf("stable-pod late failure = (%q, %q), want (started_after_resource_was_healthy, owner_condition); ok=%v", latebreak.IssueTiming, latebreak.IssueTimingBasis, ok)
	}
}

// TestJobDetectionStampsControllerOwner verifies a CronJob-owned failed Job
// carries its CronJob as the resolved owner, so job_failed rolls up to the same
// subject its pods do (their top owner is the CronJob) and the coalescing pass
// can match them.
// TestJobDetectionStaysOwnSubject pins that a failed Job is NOT rolled up to its
// CronJob owner — it stays its own subject so a failing Job's detail page keeps
// showing the issue (the pods skip the Job in the owner chain, so rolling
// job_failed up to the CronJob would fold it away from the Job).
func TestJobDetectionStaysOwnSubject(t *testing.T) {
	now := time.Now()
	controller := true
	client := fake.NewClientset(
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "nightly-run",
				Namespace:         "prod",
				CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)),
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "batch/v1", Kind: "CronJob", Name: "nightly", Controller: &controller,
				}},
			},
			Status: batchv1.JobStatus{
				Conditions: []batchv1.JobCondition{{
					Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded",
				}},
			},
		},
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()

	var owned Detection
	var okOwned bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		owned, okOwned = lookupProblem(DetectProblems(cache, "prod"), "Job", "nightly-run", "BackoffLimitExceeded")
		if okOwned {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !okOwned {
		t.Fatal("CronJob-owned failed Job not detected")
	}
	if owned.OwnerKind != "" {
		t.Errorf("a CronJob-owned failed Job must stay its own subject (no owner stamp), got owner kind %q", owned.OwnerKind)
	}
}
