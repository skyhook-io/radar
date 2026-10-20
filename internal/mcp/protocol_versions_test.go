package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/internal/server"
)

// Protocol 2026-07-28 has no initialize: a client learns the server's
// capabilities and instructions from server/discover. Older clients still
// connect with initialize. These tests speak raw JSON-RPC on both, so a change
// in what the SDK's own client negotiates cannot hide a server-side gap.
const discoverProtocolVersion = "2026-07-28"

var initializeProtocolVersions = []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// connectResult is the part of an initialize or server/discover result these
// tests check.
type connectResult struct {
	ProtocolVersion   string   `json:"protocolVersion"`
	SupportedVersions []string `json:"supportedVersions"`
	Instructions      *string  `json:"instructions"`
	ServerInfo        struct {
		Name string `json:"name"`
	} `json:"serverInfo"`
}

// connectRequest builds the request a client on protocolVersion opens with:
// initialize up to 2025-11-25, server/discover with the per-request _meta on
// 2026-07-28.
func connectRequest(protocolVersion string) (method string, params map[string]any) {
	clientInfo := map[string]any{"name": "protocol-test", "version": "test"}
	if protocolVersion < discoverProtocolVersion {
		return "initialize", map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      clientInfo,
		}
	}
	return "server/discover", map[string]any{"_meta": newProtocolMeta(protocolVersion, clientInfo)}
}

func newProtocolMeta(protocolVersion string, clientInfo map[string]any) map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    protocolVersion,
		"io.modelcontextprotocol/clientInfo":         clientInfo,
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
}

// postRPC sends one JSON-RPC request over streamable HTTP with the headers a
// client on protocolVersion sends, and decodes the response whether the server
// answers with JSON or an SSE stream.
func postRPC(t *testing.T, url, protocolVersion, method string, params map[string]any) rpcResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", protocolVersion)
	if protocolVersion >= discoverProtocolVersion {
		req.Header.Set("Mcp-Method", method)
		for _, key := range []string{"name", "uri"} {
			if name, ok := params[key].(string); ok {
				req.Header.Set("Mcp-Name", name)
			}
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var data []byte
		for line := range strings.Lines(string(raw)) {
			if rest, ok := strings.CutPrefix(line, "data: "); ok {
				data = []byte(rest)
			}
		}
		raw = data
	}
	var out rpcResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s %s at %s: HTTP %d, undecodable body %q: %v", url, method, protocolVersion, resp.StatusCode, raw, err)
	}
	return out
}

func decodeConnectResult(t *testing.T, resp rpcResponse, protocolVersion string) connectResult {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("protocol %s: error %d %q", protocolVersion, resp.Error.Code, resp.Error.Message)
	}
	var res connectResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if protocolVersion < discoverProtocolVersion {
		if res.ProtocolVersion != protocolVersion || res.ServerInfo.Name != "radar" {
			t.Errorf("initialize at %s: protocolVersion %q, serverInfo.name %q", protocolVersion, res.ProtocolVersion, res.ServerInfo.Name)
		}
	} else if !slices.Contains(res.SupportedVersions, protocolVersion) || !slices.Contains(res.SupportedVersions, "2025-11-25") {
		t.Errorf("server/discover supportedVersions = %v, want %s and the legacy versions", res.SupportedVersions, protocolVersion)
	}
	return res
}

func checkInstructions(t *testing.T, res connectResult, protocolVersion, want string) {
	t.Helper()
	switch {
	case want == "" && res.Instructions != nil:
		t.Errorf("protocol %s: got instructions %q, want none", protocolVersion, *res.Instructions)
	case want != "" && (res.Instructions == nil || *res.Instructions != want):
		t.Errorf("protocol %s: instructions differ from serverInstructions (got %v)", protocolVersion, res.Instructions != nil)
	}
}

// Every HTTP mount sends the same instructions whether the client connects
// with initialize or server/discover.
func TestHTTPInstructionsOnEveryProtocolVersion(t *testing.T) {
	refs := investigationrefs.NewRegistry()
	scope := strings.Repeat("d", 26)
	lease, err := refs.Begin(scope)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	srv := server.New(server.Config{
		DevMode:                 true,
		MCPHandler:              NewHandler(),
		MCPReadOnlyHandler:      NewReadOnlyHandler(),
		MCPInvestigationHandler: NewInvestigationHandler(refs),
	})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	for _, mount := range []struct{ path, want string }{
		{"/mcp", serverInstructions(true)},
		{"/mcp-readonly", serverInstructions(false)},
		{"/mcp-investigation?scope=" + scope, ""},
	} {
		for _, version := range append(slices.Clone(initializeProtocolVersions), discoverProtocolVersion) {
			t.Run(mount.path+"@"+version, func(t *testing.T) {
				method, params := connectRequest(version)
				res := decodeConnectResult(t, postRPC(t, httpServer.URL+mount.path, version, method, params), version)
				checkInstructions(t, res, version, mount.want)
			})
		}
	}
}

// RunStdio serves newFullServer over newline-delimited JSON-RPC; IOTransport
// is the same framing over pipes.
func TestStdioInstructionsOnEveryProtocolVersion(t *testing.T) {
	for _, version := range append(slices.Clone(initializeProtocolVersions), discoverProtocolVersion) {
		t.Run(version, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			clientToServer, serverIn := io.Pipe()
			serverOut, serverToClient := io.Pipe()
			done := make(chan error, 1)
			go func() {
				done <- newFullServer().Run(ctx, &mcpsdk.IOTransport{Reader: clientToServer, Writer: serverToClient})
			}()
			defer func() {
				serverIn.Close()
				<-done
			}()

			method, params := connectRequest(version)
			line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := serverIn.Write(append(line, '\n')); err != nil {
				t.Fatal(err)
			}
			reply, err := bufio.NewReader(serverOut).ReadBytes('\n')
			if err != nil {
				t.Fatal(err)
			}
			var resp rpcResponse
			if err := json.Unmarshal(reply, &resp); err != nil {
				t.Fatalf("undecodable reply %q: %v", reply, err)
			}
			checkInstructions(t, decodeConnectResult(t, resp, version), version, serverInstructions(true))
		})
	}
}

// The SDK client opens with server/discover when it can and initialize
// otherwise; pinning its version checks both paths end to end, including that
// each test really ran on the version it names.
func TestSDKClientGetsInstructionsOnBothProtocols(t *testing.T) {
	httpServer := httptest.NewServer(NewReadOnlyHandler())
	defer httpServer.Close()
	for _, version := range []string{"2025-11-25", discoverProtocolVersion} {
		t.Run(version, func(t *testing.T) {
			session := connectAtVersion(t, httpServer.URL, version)
			if got := session.InitializeResult().Instructions; got != serverInstructions(false) {
				t.Fatalf("instructions differ from serverInstructions(false):\n%q", got)
			}
		})
	}
}

func connectAtVersion(t *testing.T, endpoint, version string) *mcpsdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "protocol-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: endpoint}, &mcpsdk.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if got := session.InitializeResult().ProtocolVersion; got != version {
		t.Fatalf("negotiated %q, want %q", got, version)
	}
	return session
}

// Argument repair runs in receiving middleware, ahead of the SDK's schema
// validation. Check it on both protocols over the HTTP transport agents use.
func TestParamRepairOnBothProtocols(t *testing.T) {
	type sentinelIn struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace,omitempty"`
		DryRun    bool   `json:"dry_run,omitempty"`
	}
	for _, version := range []string{"2025-11-25", discoverProtocolVersion} {
		t.Run(version, func(t *testing.T) {
			received := make(chan sentinelIn, 2)
			registry := newToolParamRegistry()
			srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "radar-test", Version: "test"}, nil)
			srv.AddReceivingMiddleware(paramRepairMiddlewareFor(registry))
			addToolWithRegistry(registry, srv, &mcpsdk.Tool{Name: "get_subject_permissions", Description: "sentinel"},
				func(_ context.Context, _ *mcpsdk.CallToolRequest, in sentinelIn) (*mcpsdk.CallToolResult, any, error) {
					received <- in
					return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil, nil
				})
			httpServer := httptest.NewServer(handlerForServer(srv))
			defer httpServer.Close()
			session := connectAtVersion(t, httpServer.URL, version)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
				Name: "get_subject_permissions",
				Arguments: map[string]any{
					"namespace":   "hotel-reservation",
					"subject":     "cleanup-controller",
					"subjectKind": "ServiceAccount",
					"dryRun":      true,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError {
				t.Fatalf("repaired call was rejected: %s", renderContent(res.Content))
			}
			want := sentinelIn{Kind: "ServiceAccount", Name: "cleanup-controller", Namespace: "hotel-reservation", DryRun: true}
			if got := <-received; got != want {
				t.Errorf("handler received %+v, want %+v", got, want)
			}

			res, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
				Name:      "get_subject_permissions",
				Arguments: map[string]any{"kind": "ServiceAccount", "name": "x", "whoIsThis": "x"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if text := renderContent(res.Content); !res.IsError || !strings.Contains(text, "get_subject_permissions accepts:") {
				t.Errorf("unknown argument: isError=%v, want the accepted-arguments help:\n%s", res.IsError, text)
			}
			if len(received) != 0 {
				t.Error("the call with an unknown argument reached the handler")
			}
		})
	}
}

// apply_resource takes multi-document manifests, so one call can be larger than
// the SDK's 4 MiB default body cap while every object in it is valid.
func TestHTTPAcceptsLargeToolCalls(t *testing.T) {
	type in struct {
		YAML string `json:"yaml"`
	}
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "radar-test", Version: "test"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "fixture_apply"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, args in) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: strconv.Itoa(len(args.YAML))}}}, nil, nil
		})
	httpServer := httptest.NewServer(handlerForServer(srv))
	defer httpServer.Close()

	for _, version := range []string{"2025-11-25", discoverProtocolVersion} {
		t.Run(version, func(t *testing.T) {
			yaml := strings.Repeat("x", 6<<20)
			resp := postRPC(t, httpServer.URL, version, "tools/call", withMeta(version, map[string]any{
				"name": "fixture_apply", "arguments": map[string]any{"yaml": yaml},
			}))
			if resp.Error != nil {
				t.Fatalf("6 MiB call: error %d %q", resp.Error.Code, resp.Error.Message)
			}
			if !strings.Contains(string(resp.Result), strconv.Itoa(len(yaml))) {
				t.Fatalf("6 MiB call did not reach the tool intact: %.200s", resp.Result)
			}
		})
	}

	body := bytes.Repeat([]byte("x"), maxRequestBodyBytes+1)
	req, err := http.NewRequest(http.MethodPost, httpServer.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("body over the cap: HTTP %d, want 413", resp.StatusCode)
	}
}

// withMeta adds the per-request _meta a 2026-07-28 client sends on every
// request; earlier versions send none.
func withMeta(protocolVersion string, params map[string]any) map[string]any {
	if protocolVersion >= discoverProtocolVersion {
		params["_meta"] = newProtocolMeta(protocolVersion, map[string]any{"name": "protocol-test", "version": "test"})
	}
	return params
}

func newMCPTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := server.New(server.Config{
		DevMode:            true,
		MCPHandler:         NewHandler(),
		MCPReadOnlyHandler: NewReadOnlyHandler(),
	})
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

// Radar answers with one caller's view of one cluster, so results that carry a
// cache scope (2026-07-28) say private rather than the SDK's default public.
func TestCacheableResultsArePrivate(t *testing.T) {
	httpServer := newMCPTestServer(t)
	for _, version := range []string{"2025-11-25", discoverProtocolVersion} {
		requests := []struct {
			method string
			params map[string]any
		}{
			{"tools/list", map[string]any{}},
			{"resources/list", map[string]any{}},
			{"resources/templates/list", map[string]any{}},
			{"resources/read", map[string]any{"uri": "cluster://health"}},
		}
		if version >= discoverProtocolVersion {
			requests = append(requests, struct {
				method string
				params map[string]any
			}{"server/discover", map[string]any{}})
		}
		for _, req := range requests {
			t.Run(version+"/"+req.method, func(t *testing.T) {
				resp := postRPC(t, httpServer.URL+"/mcp", version, req.method, withMeta(version, req.params))
				if resp.Error != nil {
					t.Fatalf("error %d %q", resp.Error.Code, resp.Error.Message)
				}
				var res struct {
					CacheScope *string `json:"cacheScope"`
				}
				if err := json.Unmarshal(resp.Result, &res); err != nil {
					t.Fatal(err)
				}
				switch {
				case res.CacheScope != nil && *res.CacheScope != "private":
					t.Errorf("cacheScope = %q, want private", *res.CacheScope)
				case res.CacheScope == nil && version >= discoverProtocolVersion:
					t.Error("cacheScope missing on a 2026-07-28 result")
				}
			})
		}
	}
}

// /mcp-readonly neither lists nor runs write tools, whichever protocol the
// client speaks.
func TestReadOnlyMountRefusesWriteToolsOnBothProtocols(t *testing.T) {
	httpServer := newMCPTestServer(t)
	for _, version := range []string{"2025-11-25", discoverProtocolVersion} {
		t.Run(version, func(t *testing.T) {
			resp := postRPC(t, httpServer.URL+"/mcp-readonly", version, "tools/list", withMeta(version, map[string]any{}))
			if resp.Error != nil {
				t.Fatalf("tools/list: error %d %q", resp.Error.Code, resp.Error.Message)
			}
			var list struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(resp.Result, &list); err != nil {
				t.Fatal(err)
			}
			if len(list.Tools) == 0 {
				t.Fatal("tools/list returned no tools")
			}
			for _, tool := range list.Tools {
				if tool.Name == "patch_resource" || tool.Name == "apply_resource" {
					t.Errorf("read-only mount lists %s", tool.Name)
				}
			}

			resp = postRPC(t, httpServer.URL+"/mcp-readonly", version, "tools/call", withMeta(version, map[string]any{
				"name":      "patch_resource",
				"arguments": map[string]any{"kind": "Deployment", "namespace": "default", "name": "x", "patch": "{}"},
			}))
			if resp.Error == nil || !strings.Contains(resp.Error.Message, "unknown tool") {
				t.Fatalf("patch_resource on the read-only mount: error %+v, result %s; want unknown tool", resp.Error, resp.Result)
			}
		})
	}
}
