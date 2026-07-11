package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticatedTunnelHandlerMarksRequests(t *testing.T) {
	if IsAuthenticatedTunnelRequest(context.Background()) {
		t.Fatal("plain context must not be marked as an authenticated tunnel")
	}

	var marked bool
	handler := AuthenticatedTunnelHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marked = IsAuthenticatedTunnelRequest(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/topology", nil)
	// A lookalike header has no bearing on the private context marker.
	req.Header.Set("X-Radar-Authenticated-Tunnel", "true")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if !marked {
		t.Fatal("authenticated tunnel handler did not mark the request")
	}
	if IsAuthenticatedTunnelRequest(req.Context()) {
		t.Fatal("wrapper mutated the caller's original request context")
	}
}
