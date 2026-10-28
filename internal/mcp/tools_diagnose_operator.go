package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Operator roots have no direct Pod count: each workload bundle reports its
// own membership and collection coverage. Root reconciliation is independent.
type operatorDiagnoseResponse struct {
	Resource                any                              `json:"resource"`
	ResourceContext         *resourcecontext.ResourceContext `json:"resourceContext,omitempty"`
	RelatedIssues           []issues.Issue                   `json:"relatedIssues,omitempty"`
	Events                  []aicontext.DeduplicatedEvent    `json:"events,omitempty"`
	EventsTotalGroups       int                              `json:"eventsTotalGroups,omitempty"`
	EventsError             string                           `json:"eventsError,omitempty"`
	Warnings                []string                         `json:"warnings,omitempty"`
	OwnedWorkloads          []diagnosedWorkload              `json:"ownedWorkloads,omitempty"`
	OwnedWorkloadsTruncated bool                             `json:"ownedWorkloadsTruncated,omitempty"`
}

type diagnosedWorkload struct {
	Ref       resourcecontext.ContextRef `json:"ref"`
	Diagnosis *diagnoseResponse          `json:"diagnosis,omitempty"`
	Error     string                     `json:"error,omitempty"`
}

func operatorDiagnoseTarget(kind string) (canonical, group, resource string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "opentelemetrycollector", "opentelemetrycollectors":
		return "OpenTelemetryCollector", "opentelemetry.io", "opentelemetrycollectors", true
	}
	return "", "", "", false
}

func handleOperatorDiagnose(ctx context.Context, input diagnoseInput, kind, resource string) (*mcp.CallToolResult, any, error) {
	if !checkNamespaceAccess(ctx, input.Namespace) || !canReadInNamespace(ctx, input.Group, resource, input.Namespace, "get") {
		return nil, nil, fmt.Errorf("forbidden: cannot get %s.%s in namespace %q", resource, input.Group, input.Namespace)
	}
	cache, dynamic := k8s.GetResourceCache(), k8s.GetDynamicResourceCache()
	if cache == nil || dynamic == nil {
		return nil, nil, errNotConnected()
	}
	u, err := fetchMCPDynamicResource(ctx, cache, kind, input.Group, input.Namespace, input.Name)
	if err != nil {
		return nil, nil, err
	}
	root, ok := k8s.OwnershipObservation(u)
	if !ok || root.UID == "" {
		return nil, nil, fmt.Errorf("operator identity unavailable; retry diagnose")
	}
	minified, err := aicontext.Minify(u, aicontext.LevelDetail)
	if err != nil {
		return nil, nil, err
	}
	related := issues.RelatedIssues(issues.NewCacheProvider(), issues.RelatedIssueOptions{
		Namespaces: issueNamespacesForResource(input.Namespace), CanReadClusterScoped: issueClusterScopedAccess(ctx),
		CanReadRelated: issueRelatedResourceAccess(ctx), CanReadSubject: issueSubjectAccess(ctx), CanReadEvidence: issueEvidenceAccess(ctx),
	}, input.Group, kind, input.Namespace, input.Name)
	children := k8s.OperatorChildrenForObject(u, func(c *ownership.Catalog) ownership.Access { return ownershipAccess(ctx, c) })
	rc := buildMCPResourceContextWithOperatorChildren(ctx, u, kind, input.Namespace, input.Name, resourcecontext.TierDiagnostic, k8s.FindStaleSecretEnvChecksForObject(ctx, cache, u), nil, children)
	resp := operatorDiagnoseResponse{Resource: minified, ResourceContext: rc, RelatedIssues: related, Warnings: objectWarnings(u, rc)}
	events, total, excluded, eventErr := fetchEventsForResource(cache, kind, input.Group, input.Namespace, input.Name, u.GetUID(), "", nil, 10)
	resp.Events, resp.EventsTotalGroups = events, total
	if eventErr != nil {
		resp.EventsError = eventErr.Error()
	}
	if excluded > 0 {
		resp.Warnings = append(resp.Warnings, earlierIncarnationEventsNote(excluded, false))
	}
	if children == nil || children.Context == nil {
		resp.Warnings = append(resp.Warnings, "Owned resources could not be established. Retry diagnose after caches load; an empty runtime bundle is not proof there are no workloads.")
	} else {
		for _, ref := range children.Workloads {
			if ref.Group != "apps" || ref.Kind != "Deployment" && ref.Kind != "StatefulSet" && ref.Kind != "DaemonSet" {
				continue
			}
			if len(resp.OwnedWorkloads) == 3 {
				resp.OwnedWorkloadsTruncated = true
				break
			}
			child := input
			child.Kind, child.Group, child.Namespace, child.Name = ref.Kind, ref.Group, ref.Namespace, ref.Name
			diagnosis, childErr := diagnoseWorkload(ctx, child, &root)
			entry := diagnosedWorkload{Ref: ref, Diagnosis: diagnosis}
			if childErr != nil {
				entry.Error = boundDiagnoseMetricsError(childErr.Error())
			}
			resp.OwnedWorkloads = append(resp.OwnedWorkloads, entry)
		}
		if !children.WorkloadsComplete {
			resp.Warnings = append(resp.Warnings, "Some workload inventories could not be checked. Follow the listed references, retry after caches load, or ask a reader with broader access before ruling out other runtime workloads.")
		}
	}
	// Refuse a mixed-cluster answer or one whose root was recreated while
	// collecting child evidence. The ownership session separately validates
	// its own cache snapshot before publishing the child references.
	if k8s.GetResourceCache() != cache || k8s.GetDynamicResourceCache() != dynamic {
		return nil, nil, fmt.Errorf("connection changed while collecting operator evidence; retry diagnose")
	}
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		return nil, nil, fmt.Errorf("resource discovery unavailable; retry diagnose")
	}
	gvr, known := discovery.GetGVRWithGroup(kind, input.Group)
	if !known {
		return nil, nil, fmt.Errorf("operator inventory unavailable; retry diagnose")
	}
	current, found, readable := dynamic.ReadableObject(gvr, input.Namespace, input.Name)
	if !readable || !found || current.GetUID() != root.UID {
		return nil, nil, fmt.Errorf("operator identity changed or its inventory is unavailable; retry diagnose")
	}
	return toJSONResult(resp)
}

func ownedByOperator(obj runtime.Object, root ownership.Observation) bool {
	m, ok := obj.(metav1.Object)
	if !ok || m.GetNamespace() != root.Ref.Namespace {
		return false
	}
	for _, owner := range m.GetOwnerReferences() {
		gk := schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind).GroupKind()
		if owner.UID == root.UID && owner.Name == root.Ref.Name && gk.Group == root.Ref.Group && gk.Kind == root.Ref.Kind && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}
