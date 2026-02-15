package mcp

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/version"
)

// NewHandler creates the MCP server, registers all tools and resources,
// and returns an http.Handler to mount on chi.
func NewHandler() http.Handler {
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "radar",
			Version: version.Current,
		},
		nil,
	)

	registerTools(server)
	registerResources(server)

	return mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
}
