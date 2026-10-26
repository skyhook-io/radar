package k8s

import (
	"context"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"net/http"
	"testing"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"k8s.io/client-go/rest"
)

func TestDeletionClientSnapshotRefusesSwitchDuringClientConstruction(t *testing.T) {
	swapGlobals(t, &rest.Config{
		Host: "https://example.invalid",
		WrapTransport: func(transport http.RoundTripper) http.RoundTripper {
			CancelOngoingOperations()
			return transport
		},
	})
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "alice"})
	client, _, operation, err := DeletionClientSnapshot(ctx)
	if err == nil || client != nil || operation == nil || operation.Err() == nil {
		t.Fatalf("switched snapshot: client=%v operation=%v err=%v", client, operation, err)
	}
}

func TestDeletionPreviewRefusesChangedOperationBeforeCacheReplacement(t *testing.T) {
	operation, cancel := context.WithCancel(context.Background())
	cancel()
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "old", "namespace": "demo", "uid": "old"}}}
	p := DeletionPreview(context.Background(), obj, operation, func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() })
	if p.RootResolved || p.UnavailableReason != string(readreason.ConnectionChanged) || len(p.Dependents) != 0 {
		t.Fatalf("stale operation preview: %+v", p)
	}
}
