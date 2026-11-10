package mcp

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// A caller that leaves while Helm storage is still being read is not a Helm
// failure. Logging it as one sends whoever reads the log looking for a
// storage or API problem that isn't there.
func TestHelmIssueStreamDoesNotLogCallerLeavingAsFailure(t *testing.T) {
	release := make(chan struct{})
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_, _ = io.WriteString(w, `{"major":"1","minor":"30","gitVersion":"v1.30.0"}`)
			return
		case "/api":
			_, _ = io.WriteString(w, `{"kind":"APIVersions","versions":["v1"]}`)
			return
		case "/apis":
			_, _ = io.WriteString(w, `{"kind":"APIGroupList","apiVersion":"v1","groups":[]}`)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/secrets") {
			http.NotFound(w, r)
			return
		}
		select {
		case <-r.Context().Done():
		case <-release:
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"SecretList","items":[]}`)
		}
	}))
	t.Cleanup(storage.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	config := &rest.Config{Host: storage.URL}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	previousClient := k8s.SetTestClient(client)
	helm.ResetClient()
	if err := helm.InitializeWithRESTConfig(config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		helm.ResetClient()
		k8s.SetTestClient(previousClient)
	})

	var logs bytes.Buffer
	previousLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLog) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)
	safety := time.AfterFunc(3*time.Second, func() { close(release) })
	defer safety.Stop()
	nativeHelmIssuesForContext(ctx, []string{"shop"}, issues.Filters{})

	if strings.Contains(logs.String(), "Failed to list Helm releases") {
		t.Errorf("a caller leaving was logged as a Helm failure:\n%s", logs.String())
	}
}
