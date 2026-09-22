package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

func strimziDiagnoseKind(kind string) (string, bool) {
	switch strings.ToLower(kind) {
	case "kafkaconnector", "kafkaconnectors":
		return "KafkaConnector", true
	case "kafkaconnect", "kafkaconnects":
		return "KafkaConnect", true
	}
	return "", false
}

func handleStrimziDiagnose(ctx context.Context, input diagnoseInput, kind string) (*mcp.CallToolResult, any, error) {
	const group = "kafka.strimzi.io"
	if input.Group != "" && input.Group != group {
		return nil, nil, fmt.Errorf("invalid group %q for %s: expected %q", input.Group, kind, group)
	}
	if !checkNamespaceAccess(ctx, input.Namespace) {
		return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", input.Namespace)
	}
	resource := strings.ToLower(kind) + "s"
	if !canReadInNamespace(ctx, group, resource, input.Namespace, "get") {
		return nil, nil, fmt.Errorf("forbidden: cannot get %s.%s", resource, group)
	}
	cache, discovery := k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()
	if cache == nil || discovery == nil {
		return nil, nil, errNotConnected()
	}
	gvr, ok := discovery.GetGVRWithGroup(kind, group)
	if !ok {
		return nil, nil, fmt.Errorf("%s is not discovered; cached evidence is unavailable", kind)
	}
	if !cache.IsNamespaceSynced(gvr, input.Namespace) {
		return nil, nil, fmt.Errorf("%s cached evidence is not yet synchronized; use get_resource for an explicit resource read, then retry diagnose", kind)
	}
	obj, err := cache.GetWatched(gvr, input.Namespace, input.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s/%s is not available in the watched cache; this does not establish that it is absent. Use get_resource for an explicit resource read, then retry diagnose", kind, input.Namespace, input.Name)
	}
	// Connector configuration and raw exception traces can contain credentials.
	// Diagnosis needs only identity and the existing curated issue projection.
	resp := struct {
		Resource        any                              `json:"resource"`
		ResourceContext *resourcecontext.ResourceContext `json:"resourceContext,omitempty"`
		RelatedIssues   []issues.Issue                   `json:"relatedIssues,omitempty"`
		Coverage        string                           `json:"coverage"`
	}{
		Coverage:        "Cached operator evidence only; Pods, logs and live application endpoints were not collected.",
		Resource:        map[string]any{"apiVersion": obj.GetAPIVersion(), "kind": kind, "metadata": map[string]any{"name": obj.GetName(), "namespace": obj.GetNamespace(), "generation": obj.GetGeneration()}},
		ResourceContext: &resourcecontext.ResourceContext{Tier: resourcecontext.TierBasic, RelatedApplicationFindings: issues.CachedStrimziEvidence(obj, strimziEvidenceAccess(ctx))},
		RelatedIssues:   issues.RelatedIssues(issues.NewCacheProvider(), issues.RelatedIssueOptions{Namespaces: issueNamespacesForResource(input.Namespace), CanReadClusterScoped: issueClusterScopedAccess(ctx), CanReadRelated: issueRelatedResourceAccess(ctx)}, group, kind, input.Namespace, input.Name),
	}
	return toJSONResult(resp)
}

func strimziEvidenceAccess(ctx context.Context) func(issues.Ref) bool {
	checker := newMCPRequestScopedChecker(ctx)
	return func(ref issues.Ref) bool {
		return checkNamespaceAccess(ctx, ref.Namespace) && checker.CanRead(ctx, ref.Group, ref.Kind, ref.Namespace)
	}
}
