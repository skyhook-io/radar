package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestGetPodLogsPreservesApplicationWatcherError(t *testing.T) {
	const applicationLog = "failed to create fsnotify watcher: too many open files"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Behave like the API: prefix real application output only if requested.
		if r.URL.Query().Get("timestamps") == "true" {
			_, _ = io.WriteString(w, "2026-10-20T10:00:00Z ")
		}
		_, _ = io.WriteString(w, applicationLog+"\n")
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	result, _, err := handleGetPodLogs(context.Background(), nil, podLogsInput{Namespace: "shop", Name: "api", Container: "app"})
	if err != nil {
		t.Fatalf("application log was discarded: %v", err)
	}
	var got podLogsResponseMCP
	if err := json.Unmarshal([]byte(extractText(t, result)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 1 || got.Lines[0] != applicationLog {
		t.Fatalf("application output changed: %+v", got)
	}
}
