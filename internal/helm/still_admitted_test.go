package helm

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

// A Helm write admitted on one connection is refused if another connection
// is serving by the time it calls Helm, which builds its clients from the
// serving connection.
func TestStillAdmittedRefusesAWriteAfterTheConnectionChanged(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	k8s.RetireServingConnection()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "a"})
	admitted, err := k8s.CaptureBoundConnection()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(k8s.WithBoundConnection(t.Context(), admitted))

	rec := httptest.NewRecorder()
	if !stillAdmitted(rec, r) {
		t.Fatalf("refused on the admitted connection: %d %s", rec.Code, rec.Body)
	}
	k8s.RetireServingConnection()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "b"})
	rec = httptest.NewRecorder()
	if stillAdmitted(rec, r) || rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "context_changed") {
		t.Fatalf("after a switch: %d %s", rec.Code, rec.Body)
	}
	// Unbound callers (MCP, API) are not affected.
	rec = httptest.NewRecorder()
	if !stillAdmitted(rec, httptest.NewRequest(http.MethodPost, "/", nil)) {
		t.Fatalf("an unbound request was refused: %d", rec.Code)
	}
}
