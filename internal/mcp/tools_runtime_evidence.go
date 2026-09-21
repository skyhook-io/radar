package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	collector "github.com/skyhook-io/radar/internal/runtimeevidence"
	"github.com/skyhook-io/radar/pkg/auth"
	evidence "github.com/skyhook-io/radar/pkg/runtimeevidence"
)

type runtimeLocalCallerKey struct{}

var runtimeCollector = collector.NewCollector()

type RuntimeEvidenceInput struct {
	Adapter              string `json:"adapter" jsonschema:"rabbitmq, nats, or vault"`
	Namespace            string `json:"namespace" jsonschema:"namespace of the explicitly selected Pod"`
	Pod                  string `json:"pod" jsonschema:"name of the explicitly selected Pod; no Service or workload fanout"`
	ConfirmNetworkAccess bool   `json:"confirm_network_access" jsonschema:"must be true only after the operator authorizes endpoint collection; opens a temporary Kubernetes port-forward using existing permissions"`
}

func runtimeLocalCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		local := err == nil && ip != nil && ip.IsLoopback()
		ctx := context.WithValue(r.Context(), runtimeLocalCallerKey{}, local)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func runtimeEvidenceAllowed(ctx context.Context) bool {
	local, _ := ctx.Value(runtimeLocalCallerKey{}).(bool)
	return local && !k8s.IsInCluster() && !cloud.Mode() && !cloud.IsAuthenticatedTunnelRequest(ctx) && auth.UserFromContext(ctx) == nil
}

func handleRuntimeEvidence(ctx context.Context, _ *mcpsdk.CallToolRequest, input RuntimeEvidenceInput) (*mcpsdk.CallToolResult, any, error) {
	if !runtimeEvidenceAllowed(ctx) {
		return nil, nil, fmt.Errorf("runtime evidence collection is available only to local unauthenticated operators; hosted, authenticated and in-cluster use is unsupported")
	}
	if !input.ConfirmNetworkAccess {
		return nil, nil, fmt.Errorf("ask the operator to authorize endpoint collection from the selected Pod; retry with confirm_network_access=true only after they approve")
	}
	if len(validation.IsDNS1123Label(input.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(input.Pod)) != 0 {
		return nil, nil, fmt.Errorf("a valid namespace and Pod name are required")
	}
	adapter := evidence.Adapter(input.Adapter)
	if adapter != evidence.RabbitMQ && adapter != evidence.NATS && adapter != evidence.Vault {
		return nil, nil, fmt.Errorf("adapter must be rabbitmq, nats, or vault")
	}
	config := k8s.ConfigFromContext(ctx)
	if config == nil {
		return nil, nil, fmt.Errorf("Kubernetes connection unavailable; no runtime evidence collected")
	}
	config = rest.CopyConfig(config)
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("Kubernetes client unavailable; no runtime evidence collected")
	}
	return toJSONResult(runtimeCollector.Collect(ctx, client, config, adapter, input.Namespace, input.Pod))
}
