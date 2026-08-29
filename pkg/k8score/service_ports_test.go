package k8score

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestResolveServiceTargetPort(t *testing.T) {
	tests := []struct {
		name       string
		port       corev1.ServicePort
		containers []corev1.Container
		want       int
		wantOK     bool
	}{
		{name: "defaults to service port", port: corev1.ServicePort{Port: 9004}, want: 9004, wantOK: true},
		{name: "numeric target", port: corev1.ServicePort{Port: 9004, TargetPort: intstr.FromInt32(19004)}, want: 19004, wantOK: true},
		{
			name:       "named target",
			port:       corev1.ServicePort{Port: 9004, TargetPort: intstr.FromString("api")},
			containers: []corev1.Container{{Ports: []corev1.ContainerPort{{Name: "api", ContainerPort: 19004}}}},
			want:       19004, wantOK: true,
		},
		{name: "unresolved named target", port: corev1.ServicePort{Port: 9004, TargetPort: intstr.FromString("api")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResolveServiceTargetPort(tt.port, tt.containers)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("ResolveServiceTargetPort() = (%d, %v), want (%d, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
