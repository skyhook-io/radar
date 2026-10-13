package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

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
	DryRun      *bool  `json:"dry_run,omitempty" jsonschema:"default true: preview stored declarations and live rollback/hook evidence without writes; false requires confirm"`
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
	auditRequest := (&http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/mcp"}}).WithContext(ctx)
	audit := auth.AuditActionDetails{Action: "helm_" + options.Action, Namespace: namespace, Name: name, Source: "mcp", Outcome: "failed", Revision: options.Revision, NoHooks: &options.NoHooks, KeepHistory: &options.KeepHistory}
	if dryRun {
		audit.Outcome = "preview_failed"
	}
	defer func() { auth.AuditLogAction(auditRequest, audit) }()
	if !dryRun && input.Confirm == "" {
		return nil, nil, fmt.Errorf("confirm is required; run dry_run=true and review the preview first")
	}
	if err := helm.CheckHelmWrite(ctx, namespace); err != nil {
		return nil, nil, err
	}
	client := helm.GetClient()
	if client == nil {
		return nil, nil, fmt.Errorf("Helm client is not initialized")
	}
	if !dryRun {
		done, err := helm.BeginReleaseAction(namespace, name)
		if err != nil {
			return nil, nil, err
		}
		defer done()
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
	if options.Action == "uninstall" {
		audit.Revision = preview.Revision
	}
	if dryRun {
		if err := helm.EnrichReleaseActionPreview(ctx, cfg, namespace, preview); err != nil {
			return nil, nil, err
		}
	}
	target, err := helmConfirmationTarget(namespace, name, preview.Fingerprint)
	if err != nil {
		return nil, nil, err
	}
	result := map[string]any{"status": "ok", "dry_run": dryRun, "preview": preview, "storageNamespace": namespace}
	if dryRun {
		token, err := issueMutationConfirmationTarget(ctx, "manage_helm_release", target)
		if err != nil {
			return nil, nil, err
		}
		audit.Outcome = "preview"
		result["confirm"] = token
		result["message"] = "Review the stored release resources, action hooks, history policy, and preview limits before confirming. No changes were made."
	} else {
		if err := verifyMutationConfirmationTarget(ctx, "manage_helm_release", target, input.Confirm); err != nil {
			if errors.Is(err, ErrConfirmationTargetMismatch) {
				return nil, nil, fmt.Errorf("the release changed since the preview — preview again")
			}
			return nil, nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := consumeHelmConfirmation(input.Confirm); err != nil {
			return nil, nil, err
		}
		if err := helm.RunReleaseAction(cfg, name, options, preview.Fingerprint); err != nil {
			return nil, nil, err
		}
		audit.Outcome = "accepted"
		result["message"] = "Helm action completed. Live resource deletion and controller reconciliation can continue asynchronously; inspect get_helm_release and surviving resources."
	}
	return toJSONResult(result)
}

func helmConfirmationTarget(namespace, name, fingerprint string) (string, error) {
	raw, err := json.Marshal(struct{ Namespace, Name, Fingerprint string }{namespace, name, fingerprint})
	return string(raw), err
}

var usedHelmConfirmations = struct {
	sync.Mutex
	expires map[string]int64
}{expires: make(map[string]int64)}

func consumeHelmConfirmation(token string) error {
	key := confirmationTargetDigest(token)
	now := time.Now().Unix()
	usedHelmConfirmations.Lock()
	defer usedHelmConfirmations.Unlock()
	for k, expires := range usedHelmConfirmations.expires {
		if expires <= now {
			delete(usedHelmConfirmations.expires, k)
		}
	}
	if _, used := usedHelmConfirmations.expires[key]; used {
		return fmt.Errorf("confirm was already used to start a Helm action; preview again")
	}
	usedHelmConfirmations.expires[key] = now + 5*60
	return nil
}
