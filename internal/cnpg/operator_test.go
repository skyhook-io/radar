package cnpg

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	integration "github.com/skyhook-io/radar/internal/integration"
)

func cnpgOperatorDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cnpg-controller-manager", Namespace: "cnpg-system", Generation: 1,
			Labels: map[string]string{"app.kubernetes.io/name": "cloudnative-pg"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "cloudnative-pg"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "cloudnative-pg"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "sidecar", Image: "busybox:1.36"},
					{
						Name:    "manager",
						Image:   "ghcr.io/cloudnative-pg/cloudnative-pg:1.27.0",
						Command: []string{"/manager"},
						Args: []string{
							"controller", "--leader-elect",
							"--config-map-name=$(OPERATOR_DEPLOYMENT_NAME)-config",
							"--secret-name", "$(OPERATOR_DEPLOYMENT_NAME)-config",
						},
						Env: []corev1.EnvVar{
							{Name: "OPERATOR_DEPLOYMENT_NAME", Value: "cnpg-controller-manager"},
							{Name: "MONITORING_QUERIES_CONFIGMAP", Value: "cnpg-default-monitoring"},
						},
					},
				}},
			},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, ReadyReplicas: 1},
	}
}

func cnpgPluginDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "barman-cloud", Namespace: "cnpg-system"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "barman-cloud"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "barman-cloud"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "barman-cloud", Image: "ghcr.io/cloudnative-pg/plugin-barman-cloud:v0.5.0"},
				}},
			},
		},
	}
}

func cnpgOperatorTestPod(name string, labels map[string]string, statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cnpg-system", UID: types.UID(name + "-uid"), Labels: labels},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			StartTime:         &metav1.Time{Time: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: statuses,
		},
	}
}

func TestCNPGOperatorComponentPods(t *testing.T) {
	started := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	running := corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started)}}
	opLabels := map[string]string{"app.kubernetes.io/name": "cloudnative-pg"}
	crashed := cnpgOperatorTestPod("cnpg-controller-manager-a", opLabels,
		corev1.ContainerStatus{Name: "sidecar", RestartCount: 3, State: running, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "Completed", ExitCode: 0, FinishedAt: metav1.NewTime(started.Add(-48 * time.Hour)),
		}}},
		corev1.ContainerStatus{Name: "manager", RestartCount: 30, State: running, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(started.Add(-time.Minute)),
		}}},
	)
	steady := cnpgOperatorTestPod("cnpg-controller-manager-b", opLabels, corev1.ContainerStatus{Name: "manager", State: running})
	other := cnpgOperatorTestPod("unrelated", map[string]string{"app": "other"})
	s := newTestReader(nil)
	ctx := context.Background()
	d := cnpgOperatorDeployment()
	read := s.deploymentPods(ctx, k8sfake.NewSimpleClientset(steady, crashed, other), d)

	comp := withCNPGComponentPods(cnpgOperatorComponent(d, "operator", "", cnpgOperatorContainerOf(d)), read, cnpgOperatorContainerOf(d))
	if comp.PodCoverage == nil || comp.PodCoverage.State != "ok" || len(comp.Pods) != 2 {
		t.Fatalf("component = %+v", comp)
	}
	a, b := comp.Pods[0], comp.Pods[1]
	if a.Name != "cnpg-controller-manager-a" || !a.Ready || a.Restarts != 33 || a.StartedAt != "2026-10-04T09:00:00Z" {
		t.Errorf("crashed pod = %+v, want restarts summed over containers and the manager container's start", a)
	}
	if lt := a.LastTermination; lt == nil || lt.Container != "manager" || lt.Reason != "OOMKilled" || lt.ExitCode != 137 || lt.FinishedAt != "2026-10-04T08:59:00Z" {
		t.Errorf("lastTermination = %+v, want the most recently ended container's", a.LastTermination)
	}
	if b.Name != "cnpg-controller-manager-b" || b.Restarts != 0 || b.LastTermination != nil {
		t.Errorf("steady pod = %+v", b)
	}

	diag := cnpgOperatorPodOf(&read.pods[0])
	if diag.Restarts != 33 || diag.LastTermination == nil || diag.LastTermination.Reason != "OOMKilled" || diag.StartedAt != "2026-09-01T00:00:00Z" {
		t.Errorf("diagnosis pod = %+v, want the same restarts and termination, with the Pod's own start", diag)
	}

	notRunning := cnpgOperatorTestPod("cnpg-controller-manager-c", opLabels, corev1.ContainerStatus{Name: "manager", RestartCount: 70,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}})
	comp = withCNPGComponentPods(CNPGOperatorComponent{}, cnpgDeploymentPodRead{pods: []corev1.Pod{*notRunning}, coverage: integration.ReadSource{State: "ok"}}, cnpgOperatorContainerOf(d))
	if p := comp.Pods[0]; p.StartedAt != "" || p.Restarts != 70 {
		t.Errorf("a container that is not running has no current start: %+v", p)
	}
}

func TestCNPGOperatorComponentPodsAPIReadDenied(t *testing.T) {
	typed := k8sfake.NewSimpleClientset()
	typed.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apiForbidden("pods")
	})
	d := cnpgPluginDeployment()
	read := newTestReader(nil).deploymentPods(context.Background(), typed, d)
	comp := withCNPGComponentPods(cnpgOperatorComponent(d, "plugin", "barman-cloud.cloudnative-pg.io", firstContainer(d)), read, firstContainer(d))
	if g := comp.PodCoverage.Grant; comp.PodCoverage.State != "denied" || g == nil || g.Verb != "list" || g.Resource != "pods" || g.Namespace != "cnpg-system" {
		t.Fatalf("podCoverage = %+v, want denied naming list pods in cnpg-system", comp.PodCoverage)
	}
	if comp.Pods != nil {
		t.Errorf("pods = %+v, want none when they cannot be read", comp.Pods)
	}

}
