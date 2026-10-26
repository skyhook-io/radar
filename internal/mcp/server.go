package mcp

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/internal/version"
	"github.com/skyhook-io/radar/pkg/investigation"
)

// newServer builds a Radar MCP server. instructions is the server-instructions
// text sent in the initialize result and, on protocol 2026-07-28, the
// server/discover result; empty sends none.
func newServer(includeWrites bool, instructions string) *mcpsdk.Server {
	paramRegistry := newToolParamRegistry()
	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{
			Name:    "radar",
			Version: version.Current,
		},
		&mcpsdk.ServerOptions{
			Instructions: instructions,
			SetCacheable: privateCacheScope,
		},
	)

	// Repair recognisable argument-name mistakes before schema validation, and
	// report the accepted argument names when validation still fails. Every tool
	// schema is additionalProperties:false, so without this one wrong argument
	// name is an unrecoverable dead end for an agent. See toolparams.go.
	server.AddReceivingMiddleware(paramRepairMiddlewareFor(paramRegistry))

	registerTools(server, includeWrites, paramRegistry)
	registerResources(server)

	return server
}

// privateCacheScope marks every cacheable result (server/discover, the list
// methods, resources/read) as cacheable only by the requesting user's client.
// Radar answers with one caller's RBAC-filtered view of one cluster, so a
// shared intermediary must not serve it to anyone else; the SDK's default
// scope is "public".
func privateCacheScope(_ context.Context, _ mcpsdk.Request, c *mcpsdk.Cacheable) {
	c.CacheScope = "private"
}

// newFullServer is the public server with read and write tools, served over
// stdio and on /mcp.
func newFullServer() *mcpsdk.Server { return newServer(true, serverInstructions(true)) }

// RunStdio runs the MCP server over stdio (full tool set).
func RunStdio(ctx context.Context) error {
	return RunStdioMode(ctx, false, true)
}

// RunStdioMode uses the same read-only catalog and enforcement as HTTP.
func RunStdioMode(ctx context.Context, readOnly, allowApplicationEvidence bool) error {
	ctx = context.WithValue(ctx, runtimeLocalCallerKey{}, true)
	server := newServer(!readOnly, serverInstructions(!readOnly))
	if readOnly || !allowApplicationEvidence || !runtimeEvidenceAllowed(ctx) {
		server.RemoveTools("collect_application_evidence")
		ctx = context.WithValue(ctx, applicationActionsDisabledKey{}, true)
	}
	return server.Run(ctx, &mcpsdk.StdioTransport{})
}

// NewHandler creates the full MCP HTTP handler (read + write tools) to mount on chi.
func NewHandler() http.Handler {
	localServer, remoteServer := newFullServer(), newFullServer()
	remoteServer.RemoveTools("collect_application_evidence")
	return handlerForServerSelector(func(r *http.Request) *mcpsdk.Server {
		if runtimeEvidenceAllowed(r.Context()) {
			return localServer
		}
		return remoteServer
	})
}

func newApplyServer() *mcpsdk.Server {
	server := newServer(true, serverInstructions(true))
	server.RemoveTools("collect_application_evidence")
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			return next(context.WithValue(ctx, applicationActionsDisabledKey{}, true), method, req)
		}
	})
	return server
}

// NewApplyHandler excludes operator-only collection from built-in apply turns.
func NewApplyHandler() http.Handler { return handlerForServer(newApplyServer()) }

// NewReadOnlyHandler creates the public MCP handler exposing only read tools.
func NewReadOnlyHandler() http.Handler {
	return handlerForServer(newServer(false, serverInstructions(false)))
}

// NewInvestigationHandler creates the private read-only MCP transport used by
// Radar's built-in investigation runner. It deliberately has a separate mount
// from the public /mcp-readonly surface: the evidence marker is an internal
// correlation protocol between Radar and its agent adapters, not part of the
// normal tool result contract. It sends no server instructions: the agent
// already runs under investigation.SystemPrompt.
func NewInvestigationHandler(refs *investigationrefs.Registry) http.Handler {
	return investigationHandlerForServer(newServer(false, ""), refs)
}

func investigationHandlerForServer(server *mcpsdk.Server, refs *investigationrefs.Registry) http.Handler {
	server.AddReceivingMiddleware(investigationEvidenceReferenceMiddleware(refs))
	handler := handlerForServer(server)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope := r.URL.Query().Get("scope")
		if !investigation.ValidScope(scope) {
			http.Error(w, "invalid investigation evidence scope", http.StatusBadRequest)
			return
		}
		if !refs.Active(scope) {
			http.Error(w, "inactive investigation evidence scope", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), investigationEvidenceScopeKey{}, scope)
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

type investigationEvidenceScopeKey struct{}

func investigationEvidenceReferenceMiddleware(refs *investigationrefs.Registry) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil {
				return result, err
			}
			// server/discover replaces initialize on protocol 2026-07-28.
			if method == "initialize" || method == "server/discover" || method == "tools/list" {
				if scope, _ := ctx.Value(investigationEvidenceScopeKey{}).(string); scope != "" {
					refs.MarkConnected(scope)
				}
			}
			if method != "tools/call" {
				return result, err
			}
			toolResult, ok := result.(*mcpsdk.CallToolResult)
			if !ok {
				return result, err
			}
			scope, _ := ctx.Value(investigationEvidenceScopeKey{}).(string)
			if !investigation.ValidScope(scope) {
				return result, err
			}
			payload := investigationEvidenceProducerText(toolResult)
			if strings.TrimSpace(payload) == "" {
				return result, err
			}
			ref, issued := refs.Issue(scope, payload)
			if !issued {
				return result, err
			}
			annotateInvestigationEvidenceReference(toolResult, ref)
			return result, err
		}
	}
}

func investigationEvidenceProducerText(result *mcpsdk.CallToolResult) string {
	var payload strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcpsdk.TextContent); ok {
			payload.WriteString(text.Text)
		}
	}
	return payload.String()
}

// annotateInvestigationEvidenceReference prepends a uniform, machine-readable
// content block without changing the producer payload. All supported agent CLIs
// expose ordered text content to the model; their stream adapters remove this
// marker and retain the reference separately before persisting the tool result.
// Tool-level error results are marked too: they are not citable as causal proof,
// but their exact Radar provenance lets Findings report an honest failed check.
func annotateInvestigationEvidenceReference(result *mcpsdk.CallToolResult, ref string) {
	marker := &mcpsdk.TextContent{
		Text: investigation.RefMarker(ref),
	}
	result.Content = append([]mcpsdk.Content{marker}, result.Content...)
}

// maxRequestBodyBytes bounds one MCP request over HTTP. The SDK's default is
// 4 MiB, below what apply_resource accepts: a multi-document manifest of
// objects that are each under the API server's limit can be larger, and the
// JSON-escaped YAML grows in transit. This matches the SDK's cap on one stdio
// frame, so both transports accept the same calls.
const maxRequestBodyBytes = mcpsdk.DefaultMaxLineLength

func handlerForServer(server *mcpsdk.Server) http.Handler {
	return handlerForServerSelector(func(*http.Request) *mcpsdk.Server { return server })
}

func handlerForServerSelector(selectServer func(*http.Request) *mcpsdk.Server) http.Handler {
	streamOpts := &mcpsdk.StreamableHTTPOptions{
		Stateless:           true,
		MaxRequestBodyBytes: maxRequestBodyBytes,
	}
	// The MCP SDK auto-enables DNS-rebinding protection (Host header must be
	// loopback) when the server binds to a loopback address. That blocks
	// Docker-isolated callers reaching us via host.docker.internal. Allow
	// opt-out via env for bench setups.
	if os.Getenv("RADAR_MCP_DISABLE_LOCALHOST_PROTECTION") == "1" {
		streamOpts.DisableLocalhostProtection = true
		log.Printf("[mcp] WARNING: DNS-rebinding Host check DISABLED via env (bench mode)")
	}

	handler := runtimeLocalCaller(mcpsdk.NewStreamableHTTPHandler(
		selectServer,
		streamOpts,
	))

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
