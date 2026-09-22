package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/google/jsonschema-go/jsonschema"
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

var runtimeCollector = collector.SharedCollector()

type RuntimeEvidenceInput struct {
	Application          string `json:"application" jsonschema:"application to observe"`
	Namespace            string `json:"namespace" jsonschema:"selected Pod namespace"`
	Pod                  string `json:"pod" jsonschema:"selected Pod name"`
	PodUID               string `json:"pod_uid,omitempty" jsonschema:"expected Pod UID from a diagnosis suggestion; rejects replacement"`
	ConfirmNetworkAccess bool   `json:"confirm_network_access" jsonschema:"true only after operator authorization for temporary endpoint port-forward"`
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
	adapter := evidence.Adapter(input.Application)
	if adapter != evidence.RabbitMQ && adapter != evidence.NATS && adapter != evidence.Vault {
		return nil, nil, fmt.Errorf("application must be rabbitmq, nats, or vault")
	}
	operationCtx := k8s.OperationContext()
	if k8s.ContextOperationInProgress() {
		return nil, nil, fmt.Errorf("Kubernetes connection is changing; retry application evidence collection after it completes")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(operationCtx, cancel)
	defer stop()
	defer cancel()
	if operationCtx.Err() != nil {
		return nil, nil, fmt.Errorf("Kubernetes context changed; no application evidence retained")
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
	if operationCtx.Err() != nil {
		return nil, nil, fmt.Errorf("Kubernetes context changed; no application evidence retained")
	}
	result := runtimeCollector.CollectTarget(ctx, client, config, adapter, collector.Target{Namespace: input.Namespace, Pod: input.Pod, UID: input.PodUID})
	if operationCtx.Err() != nil {
		return nil, nil, fmt.Errorf("Kubernetes context changed; no application evidence retained")
	}
	return toJSONResult(result)
}

func applicationEvidenceInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[RuntimeEvidenceInput](nil)
	if err != nil {
		panic(err)
	}
	schema.Properties["application"].Enum = []any{"rabbitmq", "nats", "vault"}
	return schema
}
