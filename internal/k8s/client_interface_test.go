package k8s

import (
	"testing"

	"k8s.io/client-go/kubernetes"
)

func TestGetClientInterfaceIsUntypedNilWithoutClient(t *testing.T) {
	prev := SetTestClient(nil)
	t.Cleanup(func() { SetTestClient(prev) })

	if got := GetClientInterface(); got != nil {
		t.Fatalf("GetClientInterface() = %#v, want untyped nil", got)
	}
}

func TestGetClientInterfaceReturnsClient(t *testing.T) {
	c := &kubernetes.Clientset{}
	prev := SetTestClient(c)
	t.Cleanup(func() { SetTestClient(prev) })

	if got := GetClientInterface(); got != kubernetes.Interface(c) {
		t.Fatalf("GetClientInterface() = %#v, want the configured clientset", got)
	}
}
