package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
)

type manageHelmReleaseInput struct {
	Action      string `json:"action" jsonschema:"uninstall or rollback"`
	Namespace   string `json:"namespace" jsonschema:"Helm storage namespace; use storageNamespace from list_helm_releases when present"`
	Name        string `json:"name" jsonschema:"Helm release name"`
	Revision    int    `json:"revision,omitempty" jsonschema:"required positive older revision for rollback; enumerate with get_helm_release include=history"`
	NoHooks     bool   `json:"no_hooks,omitempty" jsonschema:"skip the action's hooks; may leave external resources behind"`
	KeepHistory bool   `json:"keep_history,omitempty" jsonschema:"uninstall only: retain Helm history, not Kubernetes resources; default false purges history"`
	DryRun      *bool  `json:"dry_run,omitempty" jsonschema:"default true: stored-manifest preview only, no hooks or writes; false requires confirm"`
	Confirm     string `json:"confirm,omitempty" jsonschema:"reviewed preview token; valid five minutes, bound to release snapshot, action/options, caller, and cluster context"`
}

func handleManageHelmRelease(ctx context.Context, req *mcp.CallToolRequest, input manageHelmReleaseInput) (*mcp.CallToolResult, any, error) {
	namespace, name := strings.TrimSpace(input.Namespace), strings.TrimSpace(input.Name)
	if namespace == "" || name == "" {
		return nil, nil, fmt.Errorf("namespace and name are required")
	}
	options := helm.ReleaseActionOptions{Action: strings.TrimSpace(input.Action), Revision: input.Revision, NoHooks: input.NoHooks, KeepHistory: input.KeepHistory}
	if err := options.Validate(); err != nil {
		return nil, nil, err
	}
	dryRun := input.DryRun == nil || *input.DryRun
	if !dryRun && input.Confirm == "" {
		return nil, nil, fmt.Errorf("confirm is required; run dry_run=true and review the preview first")
	}
	client := helm.GetClient()
	if client == nil {
		return nil, nil, fmt.Errorf("Helm client is not initialized")
	}
	username, groups := userFromContext(ctx)
	cfg, err := client.GetActionConfigForUser(namespace, username, groups)
	if err != nil {
		return nil, nil, err
	}
	preview, err := helm.PreviewReleaseAction(cfg, name, options)
	if err != nil {
		return nil, nil, err
	}
	target, err := helmConfirmationTarget(namespace, name, preview.Fingerprint)
	if err != nil {
		return nil, nil, err
	}
	result := map[string]any{"status": "ok", "dry_run": dryRun, "preview": preview, "storageNamespace": namespace}
	if dryRun {
		token, err := issueMutationConfirmation(ctx, "manage_helm_release", target)
		if err != nil {
			return nil, nil, err
		}
		result["confirm"] = token
		result["message"] = "Review the stored release resources, action hooks, history policy, and preview limits before confirming. No changes were made."
	} else {
		if err := verifyMutationConfirmation(ctx, "manage_helm_release", target, input.Confirm); err != nil {
			return nil, nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		err = helm.RunReleaseAction(cfg, name, options, preview.Fingerprint)
		outcome := "accepted"
		if err != nil {
			outcome = "failed"
		}
		auditRequest := (&http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/mcp"}}).WithContext(ctx)
		auth.AuditLogAction(auditRequest, auth.AuditActionDetails{Action: "helm_" + options.Action, Namespace: namespace, Name: name, Source: "mcp", Outcome: outcome})
		if err != nil {
			return nil, nil, err
		}
		result["message"] = "Helm action completed. Live resource deletion and controller reconciliation can continue asynchronously; inspect get_helm_release and surviving resources."
	}
	return toJSONResult(result)
}

func helmConfirmationTarget(namespace, name, fingerprint string) (string, error) {
	raw, err := json.Marshal(struct{ Namespace, Name, Fingerprint string }{namespace, name, fingerprint})
	return string(raw), err
}
