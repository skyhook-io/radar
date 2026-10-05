package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectServerReplacesScope(t *testing.T) {
	for _, root := range []string{"https://api.example.test", "https://api.example.test/prefix/apis/resourcemanager.miloapis.com/v1alpha1/organizations/acme/control-plane", "https://api.example.test/prefix/apis/resourcemanager.miloapis.com/v1alpha1/projects/old/control-plane"} {
		got, err := projectServer(root, "new-project")
		if err != nil {
			t.Fatal(err)
		}
		prefix := ""
		if root != "https://api.example.test" {
			prefix = "/prefix"
		}
		want := "https://api.example.test" + prefix + "/apis/resourcemanager.miloapis.com/v1alpha1/projects/new-project/control-plane"
		if got != want {
			t.Errorf("got=%q want=%q", got, want)
		}
	}
	for _, root := range []string{"http://api.example.test", "https://user:pass@api.example.test", "https://api.example.test?token=secret"} {
		if _, err := projectServer(root, "p"); err == nil {
			t.Errorf("accepted %s", root)
		}
	}
	if _, err := projectServer("https://api.example.test", "../p"); err == nil {
		t.Fatal("accepted path traversal")
	}
}

func TestProjectConfigPreservesExecAndDoesNotWriteSource(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-issued-token" {
			t.Errorf("auth header=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/projects/edge") {
			io.WriteString(w, `{"apiVersion":"resourcemanager.miloapis.com/v1alpha1","kind":"Project","metadata":{"name":"edge","uid":"project-uid"}}`)
			return
		}
		if r.URL.Path != "/apis/resourcemanager.miloapis.com/v1alpha1/projects/edge/control-plane/api/v1/namespaces" {
			t.Errorf("unexpected scoped path %s", r.URL.Path)
		}
		io.WriteString(w, `{"apiVersion":"v1","kind":"NamespaceList","items":[]}`)
	}))
	defer server.Close()
	source := filepath.Join(t.TempDir(), "config")
	cfg := clientcmdapi.NewConfig()
	cfg.CurrentContext = "parent"
	cfg.Clusters["cluster"] = &clientcmdapi.Cluster{Server: server.URL, InsecureSkipTLSVerify: true, TLSServerName: "api.example.test"}
	cfg.Contexts["parent"] = &clientcmdapi.Context{Cluster: "cluster", AuthInfo: "user", Namespace: "root-only"}
	cfg.AuthInfos["user"] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{APIVersion: "client.authentication.k8s.io/v1beta1", Command: "/bin/sh", Args: []string{"-c", `printf '%s' '{"apiVersion":"client.authentication.k8s.io/v1beta1","kind":"ExecCredential","status":{"token":"fixture-issued-token"}}'`}, InteractiveMode: clientcmdapi.NeverExecInteractiveMode}}
	if err := clientcmd.WriteToFile(*cfg, source); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	target := projectContext{Source: source, SourceName: "parent", Project: "edge", UID: "project-uid"}
	raw, err := target.loadAndVerify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if raw.AuthInfos["user"].Token != "" || raw.AuthInfos["user"].Exec == nil {
		t.Fatal("captured exec token or lost original exec config")
	}
	if raw.Clusters["cluster"].TLSServerName != "api.example.test" {
		t.Fatal("lost TLS server name")
	}
	if raw.Contexts["parent"].Namespace != "" {
		t.Fatal("inherited a root-only namespace")
	}
	after, _ := os.ReadFile(source)
	if !bytes.Equal(before, after) {
		t.Fatal("source kubeconfig changed")
	}
	target.UID = "different-project-uid"
	if _, err := target.loadAndVerify(context.Background()); err == nil {
		t.Fatal("accepted replaced project")
	}
}

func TestDatumTimelineTracksHostnameConditionsWithoutChallengeMaterial(t *testing.T) {
	old := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "networking.datumapis.com/v1alpha", "kind": "HTTPProxy", "status": map[string]any{"hostnameStatuses": []any{map[string]any{"hostname": "web.example.test", "conditions": []any{map[string]any{"type": "CertificateReady", "status": "False", "reason": "Pending", "message": "sensitive-challenge"}}}}}}}
	next := old.DeepCopy()
	items, _, _ := unstructured.NestedSlice(next.Object, "status", "hostnameStatuses")
	items[0].(map[string]any)["conditions"] = []any{map[string]any{"type": "CertificateReady", "status": "True", "reason": "CertificateIssued", "message": "sensitive-challenge"}}
	unstructured.SetNestedSlice(next.Object, items, "status", "hostnameStatuses")
	diff := ComputeDiffFromUnstructured("HTTPProxy", old, next)
	if diff == nil || len(diff.Fields) != 1 || diff.Fields[0].Path != "status.hostnameStatuses[web.example.test].conditions[CertificateReady]" {
		t.Fatalf("diff=%+v", diff)
	}
	data, _ := json.Marshal(diff)
	if bytes.Contains(data, []byte("sensitive-challenge")) {
		t.Fatal("condition message material reached Timeline")
	}
}

func TestRuntimeProjectUsesActiveRESTConfig(t *testing.T) {
	clientMu.Lock()
	oldContexts, oldName, oldPath, oldRegistry := projectContexts, contextName, kubeconfigPath, contextRegistry
	projectContexts = map[string]projectContext{"runtime-project": {}}
	contextName, kubeconfigPath, contextRegistry = "runtime-project", "/parent/config", nil
	clientMu.Unlock()
	defer func() {
		clientMu.Lock()
		projectContexts, contextName, kubeconfigPath, contextRegistry = oldContexts, oldName, oldPath, oldRegistry
		clientMu.Unlock()
	}()
	if got := GetKubeconfigPath(); got != "" {
		t.Fatalf("runtime consumer would reload parent file %q", got)
	}
}
func TestProjectSwitchRejectsChangedParentBeforeTeardown(t *testing.T) {
	clientMu.Lock()
	old := contextBinding
	contextBinding = "new-parent"
	clientMu.Unlock()
	defer func() { clientMu.Lock(); contextBinding = old; clientMu.Unlock() }()
	if err := PerformProjectContextSwitch("project", "reviewed-parent"); !errors.Is(err, ErrContextSwitchPreflight) {
		t.Fatalf("err=%v", err)
	}
}

func TestProjectVerificationHonorsCallerDeadline(t *testing.T) {
	source := filepath.Join(t.TempDir(), "config")
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["c"] = &clientcmdapi.Cluster{Server: "https://127.0.0.1:1", InsecureSkipTLSVerify: true}
	cfg.Contexts["parent"] = &clientcmdapi.Context{Cluster: "c", AuthInfo: "u"}
	cfg.AuthInfos["u"] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{APIVersion: "client.authentication.k8s.io/v1beta1", Command: "/bin/sh", Args: []string{"-c", "sleep 0.2; exit 1"}, InteractiveMode: clientcmdapi.NeverExecInteractiveMode}}
	if err := clientcmd.WriteToFile(*cfg, source); err != nil {
		t.Fatal(err)
	}
	p := projectContext{Source: source, SourceName: "parent", Project: "edge", UID: "uid"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := p.loadAndVerify(ctx); err == nil {
		t.Fatal("verification unexpectedly succeeded")
	}
	if time.Since(start) > 150*time.Millisecond {
		t.Fatal("verification remained blocked on exec plugin")
	}
	// The detached request has no shared state; let the test-owned plugin finish.
	time.Sleep(250 * time.Millisecond)
}
