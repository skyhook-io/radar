package prometheus

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/pkg/prom"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHandleConnectOptionalReturnsUnavailableStatus(t *testing.T) {
	errorlog.Reset()
	t.Cleanup(errorlog.Reset)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not prometheus", http.StatusInternalServerError)
	}))
	defer srv.Close()

	clientMu.Lock()
	previous := globalClient
	globalClient = &Client{
		manualURL:   srv.URL,
		contextName: "test-context",
		httpClient:  &http.Client{Timeout: 5 * time.Second},
	}
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		globalClient = previous
		clientMu.Unlock()
	})

	req := httptest.NewRequest(http.MethodPost, "/prometheus/connect?optional=true", nil)
	rec := httptest.NewRecorder()
	handleConnect(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var status prom.Status
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Connected {
		t.Fatal("Connected = true, want false")
	}
	if status.Error == "" {
		t.Fatal("Error is empty, want connection failure detail")
	}
	for _, entry := range errorlog.GetEntries() {
		if entry.Source == "prometheus" && entry.Level == "error" {
			t.Fatalf("optional connect recorded error log entry: %+v", entry)
		}
	}
}

func TestAnyNodeUsesDocker(t *testing.T) {
	tests := []struct {
		name    string
		nodes   []*corev1.Node
		wantHit bool
	}{
		{
			name:    "no nodes",
			nodes:   nil,
			wantHit: false,
		},
		{
			name: "containerd only",
			nodes: []*corev1.Node{
				nodeWithRuntime("node-1", "containerd://1.6.21"),
				nodeWithRuntime("node-2", "containerd://1.7.0"),
			},
			wantHit: false,
		},
		{
			name: "cri-o only",
			nodes: []*corev1.Node{
				nodeWithRuntime("node-1", "cri-o://1.27.1"),
			},
			wantHit: false,
		},
		{
			name: "docker only",
			nodes: []*corev1.Node{
				nodeWithRuntime("node-1", "docker://24.0.2"),
			},
			wantHit: true,
		},
		{
			name: "mixed — one docker node among containerd",
			nodes: []*corev1.Node{
				nodeWithRuntime("node-1", "containerd://1.6.21"),
				nodeWithRuntime("node-2", "docker://20.10.23"),
				nodeWithRuntime("node-3", "containerd://1.7.0"),
			},
			wantHit: true,
		},
		{
			name: "empty runtime string",
			nodes: []*corev1.Node{
				nodeWithRuntime("node-1", ""),
			},
			wantHit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := anyNodeUsesDocker(tt.nodes)
			if tt.wantHit && got == "" {
				t.Error("expected cri-docker hint, got empty string")
			}
			if !tt.wantHit && got != "" {
				t.Errorf("expected no hint, got: %s", got)
			}
			if tt.wantHit && got != criDockerHint {
				t.Errorf("hint text mismatch:\n  got:  %s\n  want: %s", got, criDockerHint)
			}
		})
	}
}

func nodeWithRuntime(name, runtimeVersion string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				ContainerRuntimeVersion: runtimeVersion,
			},
		},
	}
}
