package mcp

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/version"
)

func newServer() *mcpsdk.Server {
	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{
			Name:    "radar",
			Version: version.Current,
		},
		nil,
	)

	registerTools(server)
	registerResources(server)

	return server
}

// RunStdio runs the MCP server over stdio.
func RunStdio(ctx context.Context) error {
	return newServer().Run(ctx, &mcpsdk.StdioTransport{})
}

// NewHandler creates the MCP HTTP handler to mount on chi.
func NewHandler() http.Handler {
	streamOpts := &mcpsdk.StreamableHTTPOptions{Stateless: true}
	// The MCP SDK auto-enables DNS-rebinding protection (Host header must be
	// loopback) when the server binds to a loopback address. That blocks
	// Docker-isolated callers reaching us via host.docker.internal. Allow
	// opt-out via env for bench setups.
	if os.Getenv("RADAR_MCP_DISABLE_LOCALHOST_PROTECTION") == "1" {
		streamOpts.DisableLocalhostProtection = true
		log.Printf("[mcp] WARNING: DNS-rebinding Host check DISABLED via env (bench mode)")
	}

	server := newServer()
	handler := mcpsdk.NewStreamableHTTPHandler(
		func(r *http.Request) *mcpsdk.Server { return server },
		streamOpts,
	)

	// go-sdk v1.6 removed the implicit cross-origin protection default;
	// wrap the handler so a malicious page can't drive the local MCP server.
	//
	// RADAR_MCP_DISABLE_ORIGIN_PROTECTION=1 fully bypasses the wrapper. Use
	// ONLY in trusted environments where radar is reached from a known
	// non-localhost address (e.g. Docker-isolated bench agents calling via
	// host.docker.internal). Never set in user-facing installs.
	if os.Getenv("RADAR_MCP_DISABLE_ORIGIN_PROTECTION") == "1" {
		log.Printf("[mcp] WARNING: cross-origin protection DISABLED via env (bench mode)")
		return handler
	}

	prot := http.NewCrossOriginProtection()

	// Allow additional origins for browser-style callers. Does NOT affect
	// Host header validation — for Docker/external-host access use the
	// DISABLE env above. RADAR_MCP_TRUSTED_ORIGINS is a comma-separated
	// list of scheme://host[:port] entries.
	if env := strings.TrimSpace(os.Getenv("RADAR_MCP_TRUSTED_ORIGINS")); env != "" {
		for _, origin := range strings.Split(env, ",") {
			origin = strings.TrimSpace(origin)
			if origin == "" {
				continue
			}
			if err := prot.AddTrustedOrigin(origin); err != nil {
				log.Printf("[mcp] WARNING: failed to add trusted origin %q: %v", origin, err)
				continue
			}
			log.Printf("[mcp] trusted cross-origin: %s", origin)
		}
	}

	return prot.Handler(handler)
}
