package k8score

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestCreateEphemeralContainerDefaultsToGeneralSecurityContext(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "team-a",
			Name:      "app-0",
		},
	})

	ec, err := CreateEphemeralContainer(context.Background(), client, EphemeralContainerOptions{
		Namespace:       "team-a",
		PodName:         "app-0",
		TargetContainer: "app",
		Image:           "busybox:latest",
		ContainerName:   "debugger",
	})
	if err != nil {
		t.Fatalf("CreateEphemeralContainer returned error: %v", err)
	}
	if ec.SecurityContext != nil {
		t.Fatalf("returned security context = %#v, want nil", ec.SecurityContext)
	}

	updated := updatedPodFromEphemeralAction(t, client.Actions())
	if len(updated.Spec.EphemeralContainers) != 1 {
		t.Fatalf("updated pod has %d ephemeral containers, want 1", len(updated.Spec.EphemeralContainers))
	}
	got := updated.Spec.EphemeralContainers[0]
	if got.Name != "debugger" || got.TargetContainerName != "app" {
		t.Fatalf("updated ephemeral container = %#v", got)
	}
	if got.SecurityContext != nil {
		t.Fatalf("submitted security context = %#v, want nil", got.SecurityContext)
	}
}

func TestCreateEphemeralContainerRetriesWithRestrictedSecurityContextAfterPodSecurityRejection(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "team-a",
			Name:      "app-0",
		},
	})
	rejectFirstRestrictedPodSecurityUpdate(client)

	ec, err := CreateEphemeralContainer(context.Background(), client, EphemeralContainerOptions{
		Namespace:       "team-a",
		PodName:         "app-0",
		TargetContainer: "app",
		Image:           "busybox:latest",
		ContainerName:   "debugger",
	})
	if err != nil {
		t.Fatalf("CreateEphemeralContainer returned error: %v", err)
	}

	want := &corev1.SecurityContext{
		AllowPrivilegeEscalation: boolPtr(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		RunAsNonRoot: boolPtr(true),
		RunAsUser:    int64Ptr(defaultDebugRunAsUser),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
	if !reflect.DeepEqual(ec.SecurityContext, want) {
		t.Fatalf("returned security context = %#v, want %#v", ec.SecurityContext, want)
	}

	updates := updatedPodsFromEphemeralActions(t, client.Actions())
	if len(updates) != 2 {
		t.Fatalf("got %d ephemeral container updates, want 2", len(updates))
	}
	if got := updates[0].Spec.EphemeralContainers[0].SecurityContext; got != nil {
		t.Fatalf("first submitted security context = %#v, want nil", got)
	}
	if got := updates[1].Spec.EphemeralContainers[0].SecurityContext; !reflect.DeepEqual(got, want) {
		t.Fatalf("retry submitted security context = %#v, want %#v", got, want)
	}
}

func TestCreateEphemeralContainerDoesNotRetryNonRestrictedError(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "team-a",
			Name:      "app-0",
		},
	})
	client.PrependReactor("update", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "ephemeralcontainers" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "pods"},
			"app-0",
			fmt.Errorf("ephemeral containers are disabled"),
		)
	})

	_, err := CreateEphemeralContainer(context.Background(), client, EphemeralContainerOptions{
		Namespace:       "team-a",
		PodName:         "app-0",
		TargetContainer: "app",
		Image:           "busybox:latest",
		ContainerName:   "debugger",
	})
	if err == nil {
		t.Fatal("CreateEphemeralContainer returned nil error, want failure")
	}

	updates := updatedPodsFromEphemeralActions(t, client.Actions())
	if len(updates) != 1 {
		t.Fatalf("got %d ephemeral container updates, want 1", len(updates))
	}
}

func TestCreateEphemeralContainerUsesTargetContainerUserAndGroupOnRestrictedRetry(t *testing.T) {
	targetUser := int64(1001)
	targetGroup := int64(1002)
	podUser := int64(2002)
	podGroup := int64(2003)
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "team-a",
			Name:      "app-0",
		},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{
				RunAsGroup: &podGroup,
				RunAsUser:  &podUser,
			},
			Containers: []corev1.Container{{
				Name: "app",
				SecurityContext: &corev1.SecurityContext{
					RunAsGroup: &targetGroup,
					RunAsUser:  &targetUser,
				},
			}},
		},
	})
	rejectFirstRestrictedPodSecurityUpdate(client)

	ec, err := CreateEphemeralContainer(context.Background(), client, EphemeralContainerOptions{
		Namespace:       "team-a",
		PodName:         "app-0",
		TargetContainer: "app",
		Image:           "busybox:latest",
		ContainerName:   "debugger",
	})
	if err != nil {
		t.Fatalf("CreateEphemeralContainer returned error: %v", err)
	}
	if ec.SecurityContext == nil || ec.SecurityContext.RunAsUser == nil || *ec.SecurityContext.RunAsUser != targetUser {
		t.Fatalf("runAsUser = %#v, want %d", ec.SecurityContext, targetUser)
	}
	if ec.SecurityContext.RunAsGroup == nil || *ec.SecurityContext.RunAsGroup != targetGroup {
		t.Fatalf("runAsGroup = %#v, want %d", ec.SecurityContext, targetGroup)
	}
}

func TestCreateEphemeralContainerUsesPodUserAndGroupWhenTargetUnsetOnRestrictedRetry(t *testing.T) {
	podUser := int64(2002)
	podGroup := int64(2003)
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "team-a",
			Name:      "app-0",
		},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{
				RunAsGroup: &podGroup,
				RunAsUser:  &podUser,
			},
			Containers: []corev1.Container{{
				Name: "app",
			}},
		},
	})
	rejectFirstRestrictedPodSecurityUpdate(client)

	ec, err := CreateEphemeralContainer(context.Background(), client, EphemeralContainerOptions{
		Namespace:       "team-a",
		PodName:         "app-0",
		TargetContainer: "app",
		Image:           "busybox:latest",
		ContainerName:   "debugger",
	})
	if err != nil {
		t.Fatalf("CreateEphemeralContainer returned error: %v", err)
	}
	if ec.SecurityContext == nil || ec.SecurityContext.RunAsUser == nil || *ec.SecurityContext.RunAsUser != podUser {
		t.Fatalf("runAsUser = %#v, want %d", ec.SecurityContext, podUser)
	}
	if ec.SecurityContext.RunAsGroup == nil || *ec.SecurityContext.RunAsGroup != podGroup {
		t.Fatalf("runAsGroup = %#v, want %d", ec.SecurityContext, podGroup)
	}
}

func TestCreateEphemeralContainerDoesNotRetryWindowsPodWhenRestrictedContextUnavailable(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "team-a",
			Name:      "win-0",
		},
		Spec: corev1.PodSpec{
			OS: &corev1.PodOS{Name: corev1.Windows},
		},
	})
	rejectFirstRestrictedPodSecurityUpdate(client)

	_, err := CreateEphemeralContainer(context.Background(), client, EphemeralContainerOptions{
		Namespace:     "team-a",
		PodName:       "win-0",
		Image:         "mcr.microsoft.com/windows/nanoserver:ltsc2022",
		ContainerName: "debugger",
	})
	if err == nil {
		t.Fatal("CreateEphemeralContainer returned nil error, want failure")
	}

	updates := updatedPodsFromEphemeralActions(t, client.Actions())
	if len(updates) != 1 {
		t.Fatalf("got %d ephemeral container updates, want 1", len(updates))
	}
	if got := updates[0].Spec.EphemeralContainers[0].SecurityContext; got != nil {
		t.Fatalf("submitted security context = %#v, want nil", got)
	}
}

func TestCreateEphemeralContainerOmitsLinuxSecurityContextForWindowsPod(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "team-a",
			Name:      "win-0",
		},
		Spec: corev1.PodSpec{
			OS: &corev1.PodOS{Name: corev1.Windows},
		},
	})

	ec, err := CreateEphemeralContainer(context.Background(), client, EphemeralContainerOptions{
		Namespace:     "team-a",
		PodName:       "win-0",
		Image:         "mcr.microsoft.com/windows/nanoserver:ltsc2022",
		ContainerName: "debugger",
	})
	if err != nil {
		t.Fatalf("CreateEphemeralContainer returned error: %v", err)
	}
	if ec.SecurityContext != nil {
		t.Fatalf("returned security context = %#v, want nil", ec.SecurityContext)
	}

	updated := updatedPodFromEphemeralAction(t, client.Actions())
	if len(updated.Spec.EphemeralContainers) != 1 {
		t.Fatalf("updated pod has %d ephemeral containers, want 1", len(updated.Spec.EphemeralContainers))
	}
	if got := updated.Spec.EphemeralContainers[0].SecurityContext; got != nil {
		t.Fatalf("submitted security context = %#v, want nil", got)
	}
}

func updatedPodFromEphemeralAction(t *testing.T, actions []clienttesting.Action) *corev1.Pod {
	t.Helper()
	pods := updatedPodsFromEphemeralActions(t, actions)
	if len(pods) == 0 {
		t.Fatalf("no pods/ephemeralcontainers update action found in %#v", actions)
	}
	return pods[0]
}

func updatedPodsFromEphemeralActions(t *testing.T, actions []clienttesting.Action) []*corev1.Pod {
	t.Helper()
	var pods []*corev1.Pod
	for _, action := range actions {
		if action.GetVerb() != "update" || action.GetResource().Resource != "pods" || action.GetSubresource() != "ephemeralcontainers" {
			continue
		}
		update, ok := action.(clienttesting.UpdateAction)
		if !ok {
			t.Fatalf("ephemeralcontainers action has type %T, want UpdateAction", action)
		}
		pod, ok := update.GetObject().(*corev1.Pod)
		if !ok {
			t.Fatalf("ephemeralcontainers update object has type %T, want *corev1.Pod", update.GetObject())
		}
		pods = append(pods, pod)
	}
	return pods
}

func rejectFirstRestrictedPodSecurityUpdate(client *fake.Clientset) {
	attempts := 0
	client.PrependReactor("update", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "ephemeralcontainers" {
			return false, nil, nil
		}
		attempts++
		if attempts == 1 {
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Resource: "pods"},
				"app-0",
				fmt.Errorf("violates PodSecurity \"restricted:latest\": allowPrivilegeEscalation != false"),
			)
		}
		return false, nil, nil
	})
}

func int64Ptr(v int64) *int64 { return &v }
