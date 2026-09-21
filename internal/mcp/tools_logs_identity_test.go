package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestGetPodLogsContainerIdentity(t *testing.T) {
	for _, tt := range []struct {
		name          string
		container     string
		containers    []corev1.Container
		metadataCode  int
		logCode       int
		previous      bool
		shortStream   bool
		wantContainer string
	}{
		{name: "explicit application container", container: "app", wantContainer: "app"},
		{name: "identity without narrowing hint", container: "app", shortStream: true, wantContainer: "app"},
		{name: "explicit sidecar", container: "proxy", wantContainer: "proxy"},
		{name: "explicit init container", container: "init", wantContainer: "init"},
		{name: "explicit ephemeral container", container: "debug", wantContainer: "debug"},
		{name: "sole application container", containers: []corev1.Container{{Name: "app"}}, wantContainer: "app"},
		{name: "previous instance", containers: []corev1.Container{{Name: "app"}}, previous: true, wantContainer: "app"},
		{name: "metadata forbidden but logs allowed", metadataCode: http.StatusForbidden},
		{name: "ambiguous stream remains unknown", containers: []corev1.Container{{Name: "app"}, {Name: "proxy"}}},
		{name: "ambiguous stream API error preserved", containers: []corev1.Container{{Name: "app"}, {Name: "proxy"}}, logCode: http.StatusBadRequest},
		{name: "explicit container API error preserved", container: "missing", wantContainer: "missing", logCode: http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			metadataReads, logReads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/namespaces/shop/pods/api":
					metadataReads++
					w.Header().Set("Content-Type", "application/json")
					if tt.metadataCode != 0 {
						w.WriteHeader(tt.metadataCode)
						_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusFailure, Code: int32(tt.metadataCode), Message: "pod metadata forbidden"})
						return
					}
					_ = json.NewEncoder(w).Encode(corev1.Pod{
						TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
						Spec: corev1.PodSpec{
							Containers:     tt.containers,
							InitContainers: []corev1.Container{{Name: "init"}},
						},
					})
				case "/api/v1/namespaces/shop/pods/api/log":
					logReads++
					query := r.URL.Query()
					if got := query.Get("container"); got != tt.wantContainer {
						t.Errorf("requested container = %q, want %q", got, tt.wantContainer)
					}
					if (query.Get("previous") == "true") != tt.previous || query.Get("sinceSeconds") != "600" || query.Get("tailLines") != "2" {
						t.Errorf("log options not preserved: %v", query)
					}
					if tt.logCode != 0 {
						http.Error(w, "container selection failed", tt.logCode)
						return
					}
					if !tt.shortStream {
						_, _ = w.Write([]byte("INFO starting\n"))
					}
					_, _ = w.Write([]byte("ERROR missing configuration\n"))
				default:
					t.Errorf("unexpected request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			previousClient := k8s.SetTestClient(client)
			defer k8s.SetTestClient(previousClient)
			result, _, err := handleGetPodLogs(context.Background(), nil, podLogsInput{
				Namespace: "shop", Name: "api", Container: tt.container,
				Previous: tt.previous, Since: "10m", TailLines: 2, Grep: "ERROR",
			})
			wantMetadataReads := 1
			if tt.container != "" {
				wantMetadataReads = 0
			}
			if metadataReads != wantMetadataReads || logReads != 1 {
				t.Fatalf("reads: metadata=%d logs=%d", metadataReads, logReads)
			}
			if tt.logCode != 0 {
				if err == nil {
					t.Fatal("expected log API error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got podLogsResponseMCP
			if err := json.Unmarshal([]byte(extractText(t, result)), &got); err != nil {
				t.Fatal(err)
			}
			if got.Container != tt.wantContainer {
				t.Errorf("returned container = %q, want %q", got.Container, tt.wantContainer)
			}
			if len(got.Lines) != 1 || got.Lines[0] != "ERROR missing configuration" || (got.NarrowHint == "") != tt.shortStream {
				t.Errorf("filtering or narrowing lost: %+v", got)
			}
		})
	}
}
