package k8s

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestFindDuplicateEnvVarsForObject(t *testing.T) {
	t.Run("motivating duplicate reports positions values and final declaration", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment([]corev1.Container{{
			Name: "app",
			Env: []corev1.EnvVar{
				{Name: "APP_HOST", Value: "api"},
				{Name: "LOG_LEVEL", Value: "info"},
				{Name: "APP_HOST", Value: "localhost"},
			},
		}}, nil)

		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 1 {
			t.Fatalf("got %d checks, want 1: %+v", len(got), got)
		}
		check := got[0]
		if check.Container != "app" || check.EnvName != "APP_HOST" || check.LastDeclaredValue != "localhost" {
			t.Fatalf("unexpected check: %+v", check)
		}
		if len(check.Occurrences) != 2 || check.Occurrences[0].Position != 1 || check.Occurrences[0].Value != "api" || check.Occurrences[1].Position != 3 || check.Occurrences[1].Value != "localhost" {
			t.Fatalf("unexpected occurrences: %+v", check.Occurrences)
		}
		for _, want := range []string{"app", "APP_HOST", "2 times", `1="api"`, `3="localhost"`, "typically takes effect"} {
			if !strings.Contains(check.Message, want) {
				t.Errorf("message %q does not contain %q", check.Message, want)
			}
		}
	})

	t.Run("no duplicates", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment([]corev1.Container{{
			Name: "app",
			Env:  []corev1.EnvVar{{Name: "APP_HOST", Value: "api"}, {Name: "LOG_LEVEL", Value: "info"}},
		}}, nil)
		if got := FindDuplicateEnvVarsForObject(deployment); len(got) != 0 {
			t.Fatalf("got duplicate checks for unique env vars: %+v", got)
		}
	})

	t.Run("init container", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment(nil, []corev1.Container{{
			Name: "migrate",
			Env:  []corev1.EnvVar{{Name: "DB_HOST", Value: "old"}, {Name: "DB_HOST", Value: "new"}},
		}})
		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 1 || got[0].Container != "migrate" || got[0].LastDeclaredValue != "new" {
			t.Fatalf("unexpected init-container checks: %+v", got)
		}
	})

	t.Run("three declarations emit once", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment([]corev1.Container{{
			Name: "app",
			Env:  []corev1.EnvVar{{Name: "MODE", Value: "one"}, {Name: "MODE", Value: "two"}, {Name: "MODE", Value: "three"}},
		}}, nil)
		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 1 || len(got[0].Occurrences) != 3 || got[0].LastDeclaredValue != "three" {
			t.Fatalf("unexpected three-way duplicate: %+v", got)
		}
	})

	t.Run("containers are independent", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment([]corev1.Container{
			{Name: "app", Env: []corev1.EnvVar{{Name: "PORT", Value: "8080"}, {Name: "PORT", Value: "8081"}}},
			{Name: "sidecar", Env: []corev1.EnvVar{{Name: "PORT", Value: "9090"}, {Name: "PORT", Value: "9091"}}},
			{Name: "metrics", Env: []corev1.EnvVar{{Name: "PORT", Value: "9100"}}},
		}, nil)
		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 2 || got[0].Container != "app" || got[1].Container != "sidecar" {
			t.Fatalf("unexpected per-container checks: %+v", got)
		}

		deployment = duplicateEnvTestDeployment([]corev1.Container{
			{Name: "app", Env: []corev1.EnvVar{{Name: "SHARED", Value: "one"}}},
			{Name: "sidecar", Env: []corev1.EnvVar{{Name: "SHARED", Value: "two"}}},
		}, nil)
		if got := FindDuplicateEnvVarsForObject(deployment); len(got) != 0 {
			t.Fatalf("same name in separate containers must not be flagged: %+v", got)
		}
	})

	t.Run("identical values still duplicate and names are case sensitive", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment([]corev1.Container{{
			Name: "app",
			Env: []corev1.EnvVar{
				{Name: "MODE", Value: "prod"},
				{Name: "mode", Value: "lowercase"},
				{Name: "MODE", Value: "prod"},
			},
		}}, nil)
		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 1 || got[0].EnvName != "MODE" || len(got[0].Occurrences) != 2 {
			t.Fatalf("unexpected exact-name duplicate result: %+v", got)
		}
	})

	t.Run("secret values are redacted in live message", func(t *testing.T) {
		deployment := duplicateEnvTestDeployment([]corev1.Container{{
			Name: "app",
			Env: []corev1.EnvVar{
				{Name: "API_TOKEN", Value: "first-super-secret-token"},
				{Name: "API_TOKEN", Value: "second-super-secret-token"},
			},
		}}, nil)
		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 1 {
			t.Fatalf("got %d checks, want 1: %+v", len(got), got)
		}
		check := got[0]
		if check.LastDeclaredValue != "[REDACTED]" || check.Occurrences[0].Value != "[REDACTED]" || check.Occurrences[1].Value != "[REDACTED]" {
			t.Fatalf("secret values were not redacted: %+v", check)
		}
		if strings.Contains(check.Message, "first-super-secret-token") || strings.Contains(check.Message, "second-super-secret-token") || !strings.Contains(check.Message, "[REDACTED]") {
			t.Fatalf("live detection message is not safely redacted: %q", check.Message)
		}
	})

	t.Run("pathological duplicates bound message detail", func(t *testing.T) {
		env := make([]corev1.EnvVar, 0, 7)
		for _, value := range []string{"one", "two", "three", "four", "five", "six", "seven"} {
			env = append(env, corev1.EnvVar{Name: "MODE", Value: value})
		}
		deployment := duplicateEnvTestDeployment([]corev1.Container{{Name: "app", Env: env}}, nil)

		got := FindDuplicateEnvVarsForObject(deployment)
		if len(got) != 1 || len(got[0].Occurrences) != 7 || got[0].LastDeclaredValue != "seven" {
			t.Fatalf("unexpected bounded duplicate check: %+v", got)
		}
		for _, want := range []string{"7 times", `5="five"`, "... and 2 more", `last definition "seven"`} {
			if !strings.Contains(got[0].Message, want) {
				t.Errorf("message %q does not contain %q", got[0].Message, want)
			}
		}
		if strings.Contains(got[0].Message, `6="six"`) || strings.Contains(got[0].Message, `7="seven"`) {
			t.Fatalf("message contains unbounded occurrence detail: %q", got[0].Message)
		}
	})
}

func TestDetectConfigProblemsIncludesDuplicateEnvVars(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	deployment := duplicateEnvTestDeployment([]corev1.Container{{
		Name: "app",
		Env:  []corev1.EnvVar{{Name: "APP_HOST", Value: "api"}, {Name: "APP_HOST", Value: "localhost"}},
	}}, nil)
	deployment.CreationTimestamp = metav1.NewTime(now.Add(-2 * time.Hour))

	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        fake.NewClientset(deployment),
		ResourceTypes: map[string]bool{k8score.Deployments: true},
		DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &ResourceCache{ResourceCache: core}

	got := detectConfigProblems(cache, "prod", now)
	if len(got) != 1 {
		t.Fatalf("got %d config problems, want 1: %+v", len(got), got)
	}
	detection := got[0]
	if detection.Kind != "Deployment" || detection.Group != "apps" || detection.Namespace != "prod" || detection.Name != "web" {
		t.Fatalf("unexpected subject: %+v", detection)
	}
	if detection.Severity != "warning" || detection.Reason != "DuplicateEnvVar" {
		t.Fatalf("unexpected severity/reason: %+v", detection)
	}
	if detection.Fingerprint != FormatDuplicateEnvVarFingerprint("prod", "web", "app", "APP_HOST") || detection.AgeSeconds != 7200 {
		t.Fatalf("unexpected identity/age: %+v", detection)
	}
}

func duplicateEnvTestDeployment(containers, initContainers []corev1.Container) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod", CreationTimestamp: metav1.Now()},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: containers, InitContainers: initContainers}},
		},
	}
}
