package igdebug

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestStatusRetriesWithActingUserWhenSharedDiscoveryIsDenied(t *testing.T) {
	shared := fake.NewSimpleClientset()
	shared.Fake.PrependReactor("list", "daemonsets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	acting := fake.NewSimpleClientset(&appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "gadget", Namespace: "gadget", Labels: map[string]string{"k8s-app": "gadget"}},
		Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "gadget", Image: "ghcr.io/inspektor-gadget/inspektor-gadget:v0.54.1",
		}}}}},
	})
	service := NewService("")
	service.sharedClient = func() kubernetes.Interface { return shared }
	service.clientFromContext = func(context.Context) kubernetes.Interface { return acting }

	status := service.Status(context.Background())

	if status.State != StateInstalled || status.Namespace != "gadget" || status.Version != "v0.54.1" {
		t.Fatalf("status = %#v", status)
	}
}
