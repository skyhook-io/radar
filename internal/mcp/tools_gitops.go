package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/gitops"
)

// GitOps tool input types

type manageGitOpsInput struct {
	Action    string `json:"action" jsonschema:"action: sync, rollback (ArgoCD only), reconcile (FluxCD), suspend, or resume"`
	Tool      string `json:"tool" jsonschema:"gitops tool: argocd or fluxcd"`
	Kind      string `json:"kind,omitempty" jsonschema:"resource kind (FluxCD only): kustomization, helmrelease, gitrepository, etc."`
	Namespace string `json:"namespace" jsonschema:"resource namespace"`
	Name      string `json:"name" jsonschema:"resource name"`

	// ArgoCD sync options (all optional; ignored for FluxCD).
	Revision    string   `json:"revision,omitempty" jsonschema:"sync only — branch/tag/commit. Empty = use targetRevision."`
	Prune       *bool    `json:"prune,omitempty" jsonschema:"sync/rollback — delete resources no longer in source. Default true for sync, false for rollback."`
	DryRun      *bool    `json:"dryRun,omitempty" jsonschema:"sync/rollback — preview only, do not apply."`
	Force       *bool    `json:"force,omitempty" jsonschema:"sync only — kubectl --force; required for some immutable-field changes."`
	ApplyOnly   *bool    `json:"applyOnly,omitempty" jsonschema:"sync only — skip PreSync/PostSync/SyncFail hooks."`
	SyncOptions []string `json:"syncOptions,omitempty" jsonschema:"sync only — Argo SyncOption strings, e.g. Replace=true, ServerSideApply=true."`

	// ArgoCD rollback options (rollback only).
	HistoryID int64 `json:"historyId,omitempty" jsonschema:"rollback only — history entry ID to roll back to (from get_resource Application status.history)."`
}

// GitOps tool handler

func handleManageGitOps(ctx context.Context, req *mcp.CallToolRequest, input manageGitOpsInput) (*mcp.CallToolResult, any, error) {
	dynClient := k8s.DynamicClientFromContext(ctx)
	if dynClient == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	tool := strings.ToLower(input.Tool)
	action := strings.ToLower(input.Action)

	var result gitops.OperationResult
	var err error

	switch tool {
	case "argocd":
		switch action {
		case "sync":
			result, err = gitops.SyncArgoApp(ctx, dynClient, input.Namespace, input.Name, gitops.ArgoSyncOptions{
				Revision:    input.Revision,
				Prune:       input.Prune,
				DryRun:      input.DryRun,
				Force:       input.Force,
				ApplyOnly:   input.ApplyOnly,
				SyncOptions: input.SyncOptions,
			})
		case "rollback":
			if input.HistoryID <= 0 {
				return nil, nil, fmt.Errorf("rollback requires historyId (positive integer from Application status.history[].id)")
			}
			result, err = gitops.RollbackArgoApp(ctx, dynClient, input.Namespace, input.Name, gitops.ArgoRollbackOptions{
				ID:     input.HistoryID,
				Prune:  input.Prune,
				DryRun: input.DryRun,
			})
		case "suspend":
			result, err = gitops.SetArgoAutoSync(ctx, dynClient, input.Namespace, input.Name, false)
		case "resume":
			result, err = gitops.SetArgoAutoSync(ctx, dynClient, input.Namespace, input.Name, true)
		default:
			return nil, nil, fmt.Errorf("unknown ArgoCD action %q: must be sync, rollback, suspend, or resume", action)
		}

	case "fluxcd":
		if input.Kind == "" {
			return nil, nil, fmt.Errorf("kind is required for FluxCD operations (e.g. kustomization, helmrelease, gitrepository)")
		}
		entry, resolveErr := gitops.ResolveFluxKind(input.Kind)
		if resolveErr != nil {
			return nil, nil, resolveErr
		}

		switch action {
		case "reconcile":
			result, err = gitops.ReconcileFlux(ctx, dynClient, entry, input.Namespace, input.Name)
		case "suspend":
			result, err = gitops.SetFluxSuspend(ctx, dynClient, entry, input.Namespace, input.Name, true)
		case "resume":
			result, err = gitops.SetFluxSuspend(ctx, dynClient, entry, input.Namespace, input.Name, false)
		default:
			return nil, nil, fmt.Errorf("unknown FluxCD action %q: must be reconcile, suspend, or resume", action)
		}

	default:
		return nil, nil, fmt.Errorf("unknown tool %q: must be argocd or fluxcd", input.Tool)
	}

	if err != nil {
		return nil, nil, err
	}

	resp := map[string]string{
		"status":  "ok",
		"message": result.Message,
	}
	if result.RequestedAt != "" {
		resp["requestedAt"] = result.RequestedAt
	}
	return toJSONResult(resp)
}
