package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestLogSnapshotInvalidTailKeepsHTTPDefaults(t *testing.T) {
	tails := make(chan string, 16)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tails <- r.URL.Query().Get("tailLines")
		fmt.Fprintln(w, "2026-10-13T10:00:00.123456789Z default tail still applies")
	}))
	defer api.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	defer k8s.SetTestClient(previous)
	for _, path := range []struct{ url, tail string }{
		{"/api/workloads/deployments/default/nginx/logs", "100"},
		{"/api/pods/default/nginx-abc-xyz/logs?container=nginx", "500"},
	} {
		for _, invalid := range []string{"0", "-1", "invalid"} {
			sep := "?"
			if path.tail == "500" {
				sep = "&"
			}
			response, err := http.Get(testServer.URL + path.url + sep + "tailLines=" + invalid)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.StatusCode, body)
			}
			select {
			case got := <-tails:
				if got != path.tail {
					t.Fatalf("%s tail=%q, want %s", path.url, got, path.tail)
				}
			default:
				t.Fatal("no Kubernetes log request")
			}
		}
	}
}
