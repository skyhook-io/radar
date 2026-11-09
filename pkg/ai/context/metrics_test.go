package context

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestFormatPodMetrics_PodLevelBudget(t *testing.T) {
	input := &PodMetricsInput{Containers: []ContainerUsage{
		{Name: "app", CPU: "450m", Memory: "60Mi"},
		{Name: "sidecar", CPU: "2m", Memory: "4Mi"},
	}}
	podBudget := &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
	}
	own := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
	}

	t.Run("pod-level only", func(t *testing.T) {
		got := FormatPodMetrics(input, &corev1.PodSpec{
			Resources:  podBudget,
			Containers: []corev1.Container{{Name: "app"}, {Name: "sidecar"}},
		})
		want := "pod-level budget (shared by all containers): CPU/req:200m/lim:500m, Memory/req:128Mi/lim:256Mi\n" +
			"app: CPU 450m (no container request/limit; uses the pod-level budget), Memory 60Mi (no container request/limit; uses the pod-level budget)\n" +
			"sidecar: CPU 2m (no container request/limit; uses the pod-level budget), Memory 4Mi (no container request/limit; uses the pod-level budget)\n"
		if got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("mixed keeps the container's own values", func(t *testing.T) {
		got := FormatPodMetrics(input, &corev1.PodSpec{
			Resources:  podBudget,
			Containers: []corev1.Container{{Name: "app", Resources: own}, {Name: "sidecar"}},
		})
		if !strings.Contains(got, "app: CPU 450m/req:100m, Memory 60Mi/req:64Mi/lim:128Mi\n") {
			t.Fatalf("container values lost:\n%s", got)
		}
		if !strings.Contains(got, "sidecar: CPU 2m (no container request/limit; uses the pod-level budget)") {
			t.Fatalf("sidecar not tied to the pod budget:\n%s", got)
		}
		if strings.Contains(got, "no request/limit set") {
			t.Fatalf("a container under a pod budget must not read as unbounded:\n%s", got)
		}
	})

	t.Run("budget for one resource only", func(t *testing.T) {
		got := FormatPodMetrics(input, &corev1.PodSpec{
			Resources:  &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}},
			Containers: []corev1.Container{{Name: "app"}, {Name: "sidecar"}},
		})
		if !strings.HasPrefix(got, "pod-level budget (shared by all containers): Memory/lim:256Mi\n") {
			t.Fatalf("budget line:\n%s", got)
		}
		if !strings.Contains(got, "app: CPU 450m (no request/limit set), Memory 60Mi (no container request/limit; uses the pod-level budget)") {
			t.Fatalf("CPU has no budget anywhere and must still say so:\n%s", got)
		}
	})

	t.Run("container-only is unchanged", func(t *testing.T) {
		got := FormatPodMetrics(input, &corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Resources: own}, {Name: "sidecar"}}})
		want := "app: CPU 450m/req:100m, Memory 60Mi/req:64Mi/lim:128Mi\n" +
			"sidecar: CPU 2m (no request/limit set), Memory 4Mi (no request/limit set)\n"
		if got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	})
}
