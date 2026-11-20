package server

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/usagedata"
)

// Usage data names some routes literally; a renamed route would silently stop
// matching, so every one must exist on the real router.
func TestUsageDataRouteKeysExist(t *testing.T) {
	stub := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	srv := New(Config{DevMode: true, MCPHandler: stub, MCPReadOnlyHandler: stub, MCPInvestigationHandler: stub, MCPApplyHandler: stub})
	routes := chi.NewRouter()
	srv.setupAppRoutes(routes)
	have := map[string]bool{}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		have[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range usagedata.RouteKeys() {
		if !have[key] {
			t.Errorf("usage data names %q, which is not a route", key)
		}
	}
}
