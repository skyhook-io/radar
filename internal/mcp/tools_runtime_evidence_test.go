package mcp

import (
	"context"
	"k8s.io/client-go/rest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/investigation"
)

func TestRuntimeEvidenceLocalBoundary(t *testing.T) {
	defer k8s.SetTestLocalMode()()
	t.Setenv("RADAR_CLOUD_MODE", "false")
	for _, tt := range []struct {
		name, remote          string
		user, tunnel, allowed bool
	}{
		{"loopback", "127.0.0.1:4321", false, false, true},
		{"ipv6", "[::1]:4321", false, false, true},
		{"remote", "10.0.0.2:4321", false, false, false},
		{"missing peer", "", false, false, false},
		{"authenticated", "127.0.0.1:4321", true, false, false},
		{"tunnel", "127.0.0.1:4321", false, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "http://localhost/mcp", nil)
			req.RemoteAddr = tt.remote
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			if tt.user {
				req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "operator"}))
			}
			h := runtimeLocalCaller(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				if got := runtimeEvidenceAllowed(r.Context()); got != tt.allowed {
					t.Errorf("allowed=%v want %v", got, tt.allowed)
				}
			}))
			if tt.tunnel {
				h = cloud.AuthenticatedTunnelHandler(h)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
		})
	}
	ctx := context.WithValue(context.Background(), runtimeLocalCallerKey{}, true)
	t.Setenv("RADAR_CLOUD_MODE", "true")
	if runtimeEvidenceAllowed(ctx) {
		t.Fatal("Cloud mode allowed")
	}
	t.Setenv("RADAR_CLOUD_MODE", "false")
	k8s.ForceInCluster = true
	if runtimeEvidenceAllowed(ctx) {
		t.Fatal("in-cluster allowed")
	}
}

func TestRuntimeEvidenceRequiresExplicitConsentAndValidTarget(t *testing.T) {
	defer k8s.SetTestLocalMode()()
	t.Setenv("RADAR_CLOUD_MODE", "false")
	ctx := context.WithValue(context.Background(), runtimeLocalCallerKey{}, true)
	for _, input := range []RuntimeEvidenceInput{
		{Application: "nats", Namespace: "lab", Pod: "nats"},
		{Application: "nats", Namespace: "../secret", Pod: "nats", ConfirmNetworkAccess: true},
		{Application: "custom-url", Namespace: "lab", Pod: "nats", ConfirmNetworkAccess: true},
	} {
		if _, _, err := handleRuntimeEvidence(ctx, nil, input); err == nil {
			t.Fatalf("accepted invalid/unauthorized input: %+v", input)
		}
	}
}

func TestRuntimeEvidenceAbsentFromReadOnlyCatalog(t *testing.T) {
	if investigation.IsReadOnlyTool("collect_application_evidence") || investigation.IsWriteTool("collect_application_evidence") {
		t.Fatal("runtime collector exposed to built-in investigation")
	}
	for _, writes := range []bool{false, true} {
		found := false
		for _, tool := range listRegisteredToolsWith(t, writes) {
			if tool.Name == "collect_application_evidence" {
				found = true
			}
		}
		if found != writes {
			t.Fatalf("includeWrites=%v found=%v", writes, found)
		}
	}
}

func TestRuntimeEvidenceCancelsAcrossContextABA(t *testing.T) {
	defer k8s.SetTestLocalMode()()
	t.Setenv("RADAR_CLOUD_MODE", "false")
	requestStarted := make(chan struct{})
	requestCancelled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/lab/pods/vault" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		close(requestStarted)
		<-r.Context().Done()
		close(requestCancelled)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), runtimeLocalCallerKey{}, true))
	defer cancel()
	old := k8s.SetTestConfig(&rest.Config{Host: srv.URL})
	defer k8s.SetTestConfig(old)
	oldName := k8s.SetTestContextName("same-context")
	defer k8s.SetTestContextName(oldName)
	k8s.CancelOngoingOperations()
	done := make(chan error, 1)
	go func() {
		_, result, err := handleRuntimeEvidence(ctx, nil, RuntimeEvidenceInput{Application: "vault", Namespace: "lab", Pod: "vault", ConfirmNetworkAccess: true})
		if result != nil {
			t.Errorf("retained evidence after supersession: %+v", result)
		}
		done <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Pod read did not start")
	}
	k8s.CancelOngoingOperations()
	k8s.CancelOngoingOperations()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context changed") {
			t.Fatalf("wrong result: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("collection outlived cluster operation")
	}
	select {
	case <-requestCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight Kubernetes request was not cancelled")
	}
}
