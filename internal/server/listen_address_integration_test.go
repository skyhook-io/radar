package server_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/mcp"
	"github.com/skyhook-io/radar/internal/server"
)

func TestExplicitInterfaceMCPConnection(t *testing.T) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var address string
	for _, candidate := range addresses {
		ip, _, err := net.ParseCIDR(candidate.String())
		if err == nil && ip.To4() != nil && !ip.IsLoopback() && ip.IsGlobalUnicast() {
			address = ip.String()
			break
		}
	}
	if address == "" {
		t.Skip("no non-loopback IPv4 interface available")
	}
	srv := server.New(server.Config{
		ListenAddress: address, BasePath: "/radar", MCPHandler: mcp.NewHandler(),
	})
	ready := make(chan struct{})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.StartWithReady(ready) }()
	select {
	case <-ready:
	case err := <-errCh:
		t.Fatalf("start server: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	t.Cleanup(func() {
		srv.Stop()
		if err := <-errCh; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("stop server: %v", err)
		}
	})
	host, _, err := net.SplitHostPort(srv.ActualAddr())
	if err != nil || host != address {
		t.Fatalf("dial address = %q, want interface %q", srv.ActualAddr(), address)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "listen-address-test", Version: "test"}, nil)
	httpClient := &http.Client{Transport: &http.Transport{}}
	defer httpClient.CloseIdleConnections()
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint: "http://" + srv.ActualAddr() + "/radar/mcp", HTTPClient: httpClient,
	}, nil)
	if err != nil {
		t.Fatalf("MCP handshake on explicit interface: %v", err)
	}
	defer session.Close()
	result, err := session.ListTools(ctx, nil)
	if err != nil || len(result.Tools) == 0 {
		t.Fatalf("MCP tool catalog: %v, %v", result, err)
	}
}
