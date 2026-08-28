package mcp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"log"
	"net/http"
	"os"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/version"
)

func newServer(includeWrites bool) *mcpsdk.Server {
	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{
			Name:    "radar",
			Version: version.Current,
		},
		nil,
	)

	// Repair recognisable argument-name mistakes before schema validation, and
	// report the accepted argument names when validation still fails. Every tool
	// schema is additionalProperties:false, so without this one wrong argument
	// name is an unrecoverable dead end for an agent. See toolparams.go.
	server.AddReceivingMiddleware(paramRepairMiddleware)

	registerTools(server, includeWrites)
	registerResources(server)

	return server
}

// RunStdio runs the MCP server over stdio (full tool set).
func RunStdio(ctx context.Context) error {
	return newServer(true).Run(ctx, &mcpsdk.StdioTransport{})
}

// NewSessionToken returns a cryptographically random token for one Radar process.
func NewSessionToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// NewHandler creates the full MCP HTTP handler (read + write tools) to mount on
// chi. A non-empty token requires clients to send it as an Authorization bearer
// token. An empty token adds no authentication here; local sessions keep the
// default behavior, while shared deployments use Radar's proxy/OIDC middleware.
func NewHandler(token string) http.Handler {
	handler := handlerForServer(newServer(true))
	if token == "" {
		return handler
	}
	return requireBearerToken(token, handler)
}

// NewReadOnlyHandler creates an MCP handler exposing only read tools. Radar points
// read-only AI investigations here so a mutating tool can't even be discovered —
// server-side enforcement that doesn't depend on the agent CLI restricting itself.
func NewReadOnlyHandler() http.Handler { return handlerForServer(newServer(false)) }

func requireBearerToken(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="radar-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handlerForServer(server *mcpsdk.Server) http.Handler {
	streamOpts := &mcpsdk.StreamableHTTPOptions{Stateless: true}
	// The MCP SDK auto-enables DNS-rebinding protection (Host header must be
	// loopback) when the server binds to a loopback address. That blocks
	// Docker-isolated callers reaching us via host.docker.internal. Allow
	// opt-out via env for bench setups.
	if os.Getenv("RADAR_MCP_DISABLE_LOCALHOST_PROTECTION") == "1" {
		streamOpts.DisableLocalhostProtection = true
		log.Printf("[mcp] WARNING: DNS-rebinding Host check DISABLED via env (bench mode)")
	}

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
