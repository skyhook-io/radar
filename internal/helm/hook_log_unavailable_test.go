package helm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// A hook pod whose container the node has already removed answers with the
// kubelet's notice. It is evidence that the logs are gone, not a log line.
func TestFetchHookLogReportsLogsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("unable to retrieve container logs for containerd://6e2396b58f81ea5e"))
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := fetchHookLog(context.Background(), client, "apps", "migrate-abc", "migrate", false)
	if !ok || got.Error != k8score.ErrLogsUnavailable.Error() || len(got.Lines) != 0 {
		t.Fatalf("got %+v, %v", got, ok)
	}
}
