package mcp

import (
	"context"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/internal/version"
)

func newServer(includeWrites bool) *mcpsdk.Server {
	paramRegistry := newToolParamRegistry()
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
	server.AddReceivingMiddleware(paramRepairMiddlewareFor(paramRegistry))

	registerTools(server, includeWrites, paramRegistry)
	registerResources(server)

	return server
}

// RunStdio runs the MCP server over stdio (full tool set).
func RunStdio(ctx context.Context) error {
	return newServer(true).Run(ctx, &mcpsdk.StdioTransport{})
}

// NewHandler creates the full MCP HTTP handler (read + write tools) to mount on chi.
func NewHandler() http.Handler { return handlerForServer(newServer(true)) }

// NewReadOnlyHandler creates the public MCP handler exposing only read tools.
func NewReadOnlyHandler() http.Handler { return handlerForServer(newServer(false)) }

// NewInvestigationHandler creates the private read-only MCP transport used by
// Radar's built-in investigation runner. It deliberately has a separate mount
// from the public /mcp-readonly surface: the evidence marker is an internal
// correlation protocol between Radar and its agent adapters, not part of the
// normal tool result contract.
func NewInvestigationHandler(refs *investigationrefs.Registry) http.Handler {
	return investigationHandlerForServer(newServer(false), refs)
}

func investigationHandlerForServer(server *mcpsdk.Server, refs *investigationrefs.Registry) http.Handler {
	server.AddReceivingMiddleware(investigationEvidenceReferenceMiddleware(refs))
	handler := handlerForServer(server)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope := r.URL.Query().Get("scope")
		if !investigationEvidenceScopeRe.MatchString(scope) {
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

const (
	investigationEvidenceMarkerPrefix = "[[radar:evidence-ref="
	investigationEvidenceMarkerSuffix = "]]\n"
)

var investigationEvidenceScopeRe = regexp.MustCompile(`^[a-z2-7]{26,128}$`)

type investigationEvidenceScopeKey struct{}

func investigationEvidenceReferenceMiddleware(refs *investigationrefs.Registry) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil || method != "tools/call" {
				return result, err
			}
			toolResult, ok := result.(*mcpsdk.CallToolResult)
			if !ok {
				return result, err
			}
			scope, _ := ctx.Value(investigationEvidenceScopeKey{}).(string)
			if !investigationEvidenceScopeRe.MatchString(scope) {
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
		Text: investigationEvidenceMarkerPrefix + ref + investigationEvidenceMarkerSuffix,
	}
	result.Content = append([]mcpsdk.Content{marker}, result.Content...)
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
