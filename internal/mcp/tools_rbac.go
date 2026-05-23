package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/rbac"
)

// MCP tool input/output for get_subject_permissions.
//
// The shape is deliberately *terser* than the HTTP /api/rbac/subject/...
// response: an LLM walking RBAC for an incident wants the answer to "what
// can this principal do" without paying token cost for binding-level
// provenance it can re-request. We keep:
//   - subject identity (echoed for clarity)
//   - bindings: one row per granting binding with role identity + rule count
//   - flat rules: deduplicated, capped (the load-bearing answer)
//   - usedByPods: name list, capped, with totalCount when truncated
//
// We drop:
//   - per-binding rule blowup (use the dedicated `get_resource` on the
//     specific Role/ClusterRole to inspect)
//   - InheritedFromGroup attribution (the AI can ask follow-up if needed)

type subjectPermissionsInput struct {
	Kind      string `json:"kind" jsonschema:"subject kind: ServiceAccount, User, or Group"`
	Namespace string `json:"namespace,omitempty" jsonschema:"namespace (required for ServiceAccount, omit for User/Group)"`
	Name      string `json:"name" jsonschema:"subject name"`
}

type subjectPermissionsResult struct {
	Subject    mcpSubject          `json:"subject"`
	Bindings   []mcpBindingLite    `json:"bindings"`
	FlatRules  []rbacv1.PolicyRule `json:"flatRules"`
	Truncated  bool                `json:"truncated,omitempty"`
	UsedByPods []string            `json:"usedByPods,omitempty"` // "ns/name" pairs
	PodsTotal  int                 `json:"podsTotal,omitempty"`  // >0 when usedByPods was truncated
}

type mcpSubject struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// mcpBindingLite is the per-binding row in the MCP response. Just enough to
// identify the binding and the role it grants; rule details are accessible
// via get_resource on the role.
type mcpBindingLite struct {
	BindingKind      string `json:"bindingKind"` // "RoleBinding" | "ClusterRoleBinding"
	BindingNamespace string `json:"bindingNamespace,omitempty"`
	BindingName      string `json:"bindingName"`
	RoleKind         string `json:"roleKind"` // "Role" | "ClusterRole"
	RoleNamespace    string `json:"roleNamespace,omitempty"`
	RoleName         string `json:"roleName"`
	// RulesCount avoids inlining the rules (the AI can fetch the role for
	// detail). Useful for ranking: "the binding with 12 rules is the
	// permissive one."
	RulesCount int `json:"rulesCount"`
	// InheritedFromGroup is set when the binding came in via implicit
	// group membership (system:authenticated etc.) — distinguishes
	// "this subject was directly granted" from "this subject inherited".
	InheritedFromGroup string `json:"inheritedFromGroup,omitempty"`
}

const mcpPodsListCap = 50

const rbacAuthzGroup = "rbac.authorization.k8s.io"

func handleGetSubjectPermissions(ctx context.Context, _ *mcp.CallToolRequest, input subjectPermissionsInput) (*mcp.CallToolResult, any, error) {
	if input.Kind == "" || input.Name == "" {
		return nil, nil, fmt.Errorf("kind and name are required")
	}
	if input.Kind != "ServiceAccount" && input.Kind != "User" && input.Kind != "Group" {
		return nil, nil, fmt.Errorf("unsupported kind %q (want ServiceAccount, User, or Group)", input.Kind)
	}
	if input.Kind == "ServiceAccount" && input.Namespace == "" {
		return nil, nil, fmt.Errorf("ServiceAccount requires a namespace")
	}

	// Mirror the REST requireRBACReadable gate: both list permissions
	// must succeed, otherwise a partial reverse-lookup would mislead
	// the caller (and leak binding names the user can't see directly).
	if !canReadInNamespace(ctx, rbacAuthzGroup, "rolebindings", "", "list") {
		return nil, nil, fmt.Errorf("requires list permission on rolebindings (rbac.authorization.k8s.io) to compute reverse-lookup")
	}
	if !canReadClusterScopedKind(ctx, "clusterrolebindings", rbacAuthzGroup, "list") {
		return nil, nil, fmt.Errorf("requires list permission on clusterrolebindings (rbac.authorization.k8s.io) to compute reverse-lookup")
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	// Build the index inline. The HTTP handler memoizes a singleton; MCP
	// callers come in at different rates and we'd rather rebuild (~ms) than
	// share state across the two paths.
	//
	// Listers return nil when the corresponding RBAC informer hasn't synced
	// or RBAC denies the read — calling .List on nil panics. Guard each
	// before dereferencing; if any of the four is unavailable, the index
	// would be partial and misleading, so surface that as an error rather
	// than silently return an under-counted permission set.
	roleLister := cache.Roles()
	clusterRoleLister := cache.ClusterRoles()
	roleBindingLister := cache.RoleBindings()
	clusterRoleBindingLister := cache.ClusterRoleBindings()
	if roleLister == nil || clusterRoleLister == nil || roleBindingLister == nil || clusterRoleBindingLister == nil {
		return nil, nil, fmt.Errorf("RBAC cache not available (informers not synced or RBAC reads disabled for the Radar SA)")
	}
	roles, _ := roleLister.List(labels.Everything())
	clusterRoles, _ := clusterRoleLister.List(labels.Everything())
	roleBindings, _ := roleBindingLister.List(labels.Everything())
	clusterRoleBindings, _ := clusterRoleBindingLister.List(labels.Everything())
	idx := rbac.BuildIndex(roles, clusterRoles, roleBindings, clusterRoleBindings)

	subj := rbac.Subject{Kind: input.Kind, Namespace: input.Namespace, Name: input.Name}
	er := idx.EffectiveRules(subj)

	result := subjectPermissionsResult{
		Subject: mcpSubject{
			Kind:      subj.Kind,
			Namespace: subj.Namespace,
			Name:      subj.Name,
		},
		Bindings:  make([]mcpBindingLite, 0, len(er.ViaBindings)),
		FlatRules: er.Flat,
		Truncated: er.Truncated,
	}

	for _, br := range er.ViaBindings {
		result.Bindings = append(result.Bindings, mcpBindingLite{
			BindingKind:        br.Binding.Kind,
			BindingNamespace:   br.Binding.Namespace,
			BindingName:        br.Binding.Name,
			RoleKind:           br.Role.Kind,
			RoleNamespace:      br.Role.Namespace,
			RoleName:           br.Role.Name,
			RulesCount:         len(br.Rules),
			InheritedFromGroup: br.InheritedFromGroup,
		})
	}

	// Optional Pod consumers — only for ServiceAccount subjects. Gate on
	// the caller's own pod-list permission in the SA's namespace: without
	// this, a user with binding-list access but no pod-list access could
	// enumerate pod names in (e.g.) kube-system by querying SAs there.
	if subj.Kind == "ServiceAccount" && canReadInNamespace(ctx, "", "pods", subj.Namespace, "list") {
		pods := cache.Pods()
		if pods != nil {
			all, err := pods.Pods(subj.Namespace).List(labels.Everything())
			if err == nil {
				matched := make([]string, 0)
				for _, p := range all {
					saName := p.Spec.ServiceAccountName
					if saName == "" {
						saName = "default"
					}
					if saName == subj.Name {
						matched = append(matched, p.Namespace+"/"+p.Name)
					}
				}
				total := len(matched)
				if total > mcpPodsListCap {
					result.UsedByPods = matched[:mcpPodsListCap]
					result.PodsTotal = total
				} else {
					result.UsedByPods = matched
				}
			}
		}
	}

	return toJSONResult(result)
}
