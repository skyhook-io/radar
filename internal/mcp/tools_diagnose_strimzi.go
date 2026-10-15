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
		OperatorSnapshot issues.StrimziSnapshotAssessment `json:"operatorSnapshot"`
		Resource         any                              `json:"resource"`
		ResourceContext  *resourcecontext.ResourceContext `json:"resourceContext,omitempty"`
		RelatedIssues    []issues.Issue                   `json:"relatedIssues,omitempty"`
		Coverage         string                           `json:"coverage"`
	}{
		OperatorSnapshot: issues.AssessStrimziSnapshot(gvr, obj),
		Coverage:         "Cached operator evidence only; Pods, logs and live application endpoints were not collected.",
		Resource:         map[string]any{"apiVersion": obj.GetAPIVersion(), "kind": kind, "metadata": map[string]any{"name": obj.GetName(), "namespace": obj.GetNamespace(), "generation": obj.GetGeneration()}},
		ResourceContext:  &resourcecontext.ResourceContext{Tier: resourcecontext.TierBasic, RelatedApplicationFindings: issues.CachedStrimziEvidence(obj, issueRelatedResourceAccess(ctx))},
	}
	if kind == "KafkaConnector" {
		rows := issues.RelatedIssues(issues.NewCacheProvider(), issues.RelatedIssueOptions{Namespaces: issueNamespacesForResource(input.Namespace), CanReadClusterScoped: issueClusterScopedAccess(ctx), CanReadRelated: issueRelatedResourceAccess(ctx), CanReadSubject: issueSubjectAccess(ctx), CanReadEvidence: issueEvidenceAccess(ctx)}, group, kind, input.Namespace, input.Name)
		for _, row := range rows {
			if row.Group != group || row.Kind != kind || row.Namespace != input.Namespace || row.Name != input.Name {
				continue
			}
			switch row.Reason {
			case "StrimziConnectorFailed", "StrimziConnectorTasksFailed", "StrimziConnectorNotReady":
				resp.RelatedIssues = append(resp.RelatedIssues, row)
			}
		}
	}
	if kind == "KafkaConnect" {
		phase := map[string]string{"ready": "Ready", "not_ready": "NotReady", "unknown": "Unknown", "paused": "Paused", "terminating": "Terminating"}[resp.OperatorSnapshot.Readiness]
		resp.ResourceContext.StatusSummary = &resourcecontext.StatusSummary{Phase: phase}
		if resp.OperatorSnapshot.Readiness == "ready" || resp.OperatorSnapshot.Readiness == "not_ready" {
			status := "True"
			if resp.OperatorSnapshot.Readiness == "not_ready" {
				status = "False"
			}
			resp.ResourceContext.StatusSummary.Conditions = []resourcecontext.ConditionSummary{{Type: "Ready", Status: status}}
		}
	}
	return toJSONResult(resp)
}
