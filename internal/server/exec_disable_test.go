package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestDisableExecRejectsEveryExecBackedEndpoint(t *testing.T) {
	previous := k8s.ForceDisableExec
	t.Cleanup(func() {
		k8s.ForceDisableExec = previous
	})
	k8s.ForceDisableExec = true

	server := &Server{}
	tests := []struct {
		name    string
		method  string
		path    string
		handler http.HandlerFunc
	}{
		{name: "pod terminal", method: http.MethodGet, path: "/api/pods/ns/pod/exec", handler: server.handlePodExec},
		{name: "pod files", method: http.MethodGet, path: "/api/pods/ns/pod/files", handler: server.handlePodFileList},
		{name: "pod file download", method: http.MethodGet, path: "/api/pods/ns/pod/files/download", handler: server.handlePodFileDownload},
		{name: "pod file save", method: http.MethodPost, path: "/api/pods/ns/pod/files/save", handler: server.handlePodFileSave},
		{name: "node debug", method: http.MethodPost, path: "/api/nodes/node/debug", handler: server.handleNodeDebug},
		{name: "ephemeral debug container", method: http.MethodPost, path: "/api/pods/ns/pod/debug", handler: server.handleCreateDebugContainer},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tt.handler(w, httptest.NewRequest(tt.method, tt.path, nil))

			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
			}
			if got, want := w.Body.String(), "{\"error\":\"exec is disabled\"}\n"; got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
		})
	}
}
