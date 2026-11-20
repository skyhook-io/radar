package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// The issues tool's version of the REST check: a caller who can read Helm's
// storage in team-a only is told team-b wasn't checked, rather than getting an
// answer that reads as nothing wrong there.
func TestIssuesToolSaysWhichNamespacesHelmWasNotCheckedIn(t *testing.T) {
	client := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	discovered := make(chan struct{})
	dc := k8s.GetDynamicResourceCache()
	dc.OnCRDDiscoveryComplete(func() { close(discovered) })
	dc.DiscoverAllCRDs()
	select {
	case <-discovered:
	case <-time.After(5 * time.Second):
		t.Fatal("empty CRD discovery did not complete")
	}
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/secrets") || !strings.Contains(r.URL.RawQuery, "owner") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.URL.Path, "/namespaces/team-a/") {
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"SecretList","items":[]}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`)
	}))
	t.Cleanup(storage.Close)
	helm.ResetClient()
	if err := helm.InitializeWithRESTConfig(&rest.Config{Host: storage.URL}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(helm.ResetClient)

	perms := &pkgauth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "", "secrets", "", false)
	perms.SetCanI("list", "", "secrets", "team-a", true)
	perms.SetCanI("list", "", "secrets", "team-b", false)
	getPermCache().Set("mixed", nil, perms)
	t.Cleanup(getPermCache().Invalidate)

	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "mixed"})
	result, _, err := handleIssuesTool(ctx, nil, issuesInput{})
	if err != nil {
		t.Fatalf("handleIssuesTool: %v", err)
	}
	var wire struct {
		Unchecked []map[string]any `json:"unchecked_sources"`
	}
	if err := json.Unmarshal([]byte(extractText(t, result)), &wire); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var helmEntries []map[string]any
	for _, entry := range wire.Unchecked {
		if entry["source"] == "helm" {
			helmEntries = append(helmEntries, entry)
		}
	}
	want := []map[string]any{{"source": "helm", "reason": "caller_access_denied", "namespaces": []any{"team-b"}}}
	if !reflect.DeepEqual(helmEntries, want) {
		t.Fatalf("helm unchecked = %v, want %v", helmEntries, want)
	}
}
