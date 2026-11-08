package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/Masterminds/semver/v3"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"helm.sh/helm/v3/pkg/release"
)

type manageHelmInput struct {
	Action           string         `json:"action" jsonschema:"install, upgrade, rollback, or uninstall"`
	Namespace        string         `json:"namespace" jsonschema:"release storage namespace (target namespace for install)"`
	Name             string         `json:"name" jsonschema:"Helm release name"`
	Chart            string         `json:"chart,omitempty" jsonschema:"chart name; required for install"`
	Repository       string         `json:"repository,omitempty" jsonschema:"install: required configured repository name, URL or OCI prefix. upgrade: optional configured repository name; must match the release's confirmed saved source"`
	Version          string         `json:"version,omitempty" jsonschema:"exact chart version; required for install; upgrade choices: get_helm_release include=versions; omission keeps the stored version only when latest and deployed history agree"`
	DryRun           bool           `json:"dry_run,omitempty" jsonschema:"upgrade only: render a non-mutating advisory preview with the same values merge and caller lookups; default false executes"`
	ExpectedRevision int            `json:"expected_revision,omitempty" jsonschema:"upgrade only: refuse if latest release revision differs; use revision from get_helm_release or currentRevision from preview; not an atomic lock"`
	Values           map[string]any `json:"values,omitempty" jsonschema:"install values or upgrade overrides; merge uses the deployed baseline, not necessarily the latest values read; replace_values=true resets overrides; no redacted output or literal credentials"`
	ReplaceValues    bool           `json:"replace_values,omitempty" jsonschema:"upgrade only: explicitly replace ALL stored overrides with values, including an empty object to reset; default false merges maps and refuses hidden-credential container replacement; true drops omitted overrides, including hidden credentials"`
	Revision         int            `json:"revision,omitempty" jsonschema:"positive target revision; required for rollback; inspect get_helm_release include=history first"`
	Confirm          string         `json:"confirm,omitempty" jsonschema:"uninstall only: omit to preview release resources; pass the reviewed preview token to uninstall (expires after 5 minutes)"`
}

func (in manageHelmInput) validate() error {
	if len(validation.IsDNS1123Label(in.Namespace)) > 0 {
		return fmt.Errorf("a valid namespace is required")
	}
	if len(validation.IsDNS1123Subdomain(in.Name)) > 0 || len(in.Name) > 53 {
		return fmt.Errorf("a valid Helm release name (at most 53 characters) is required")
	}
	switch in.Action {
	case "install":
		if in.Chart == "" || in.Repository == "" || in.Version == "" {
			return fmt.Errorf("install requires chart, repository and version")
		}
	case "upgrade":
	case "rollback":
		if in.Revision <= 0 {
			return fmt.Errorf("rollback requires a positive revision from get_helm_release include=history")
		}
	case "uninstall":
	default:
		return fmt.Errorf("action must be install, upgrade, rollback, or uninstall")
	}
	if in.Action != "install" && in.Chart != "" {
		return fmt.Errorf("chart is only valid for install")
	}
	if in.Action != "install" && in.Action != "upgrade" && (in.Repository != "" || in.Version != "" || in.Values != nil) {
		return fmt.Errorf("repository, version and values are only valid for install or upgrade")
	}
	if in.Action != "rollback" && in.Revision != 0 {
		return fmt.Errorf("revision is only valid for rollback")
	}
	if in.Action != "uninstall" && in.Confirm != "" {
		return fmt.Errorf("confirm is only valid for uninstall")
	}
	if in.ExpectedRevision < 0 || (in.Action != "upgrade" && (in.DryRun || in.ExpectedRevision != 0)) {
		return fmt.Errorf("dry_run and a positive expected_revision are only valid for upgrade")
	}
	if in.Version != "" {
		if _, err := semver.StrictNewVersion(strings.TrimPrefix(in.Version, "v")); err != nil {
			return fmt.Errorf("version must be an exact chart version, not a range or latest")
		}
	}
	if in.ReplaceValues && (in.Action != "upgrade" || in.Values == nil) {
		return fmt.Errorf("replace_values requires action=upgrade and an explicit values object")
	}
	if in.Values != nil {
		encoded, err := json.Marshal(in.Values)
		if err != nil {
			return fmt.Errorf("invalid values: %w", err)
		}
		if strings.Contains(string(encoded), "[REDACTED]") {
			return fmt.Errorf("values contain redaction placeholders; do not apply redacted output from get_helm_release")
		}
	}
	return nil
}

func handleManageHelm(ctx context.Context, _ *mcp.CallToolRequest, in manageHelmInput) (*mcp.CallToolResult, any, error) {
	r := (&http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/mcp"}}).WithContext(ctx)
	audit := auth.AuditActionDetails{Action: "helm_" + in.Action, Kind: "HelmRelease", Namespace: in.Namespace, Name: in.Name, Source: "mcp", Outcome: "refused"}
	defer func() { auth.AuditLogAction(r, audit) }()
	if err := in.validate(); err != nil {
		return nil, nil, err
	}
	if err := requireConnected(); err != nil {
		return nil, nil, err
	}
	operation := k8s.OperationContext()
	config, clusterContext := k8s.GetConfigSnapshot()
	ctx = context.WithValue(ctx, confirmationContextKey{}, clusterContext)
	if _, err := helm.CheckWritePermission(ctx, in.Namespace); err != nil {
		return nil, nil, err
	}
	if config == nil || operation.Err() != nil || k8s.ActiveClusterContext() != clusterContext {
		return nil, nil, fmt.Errorf("cluster connection changed or is unavailable; retry after reconnecting")
	}
	// Pin every read and write in this call to the same cluster, even if the
	// local operator switches contexts while Helm downloads a chart.
	client := helm.NewStandaloneClient(config)
	user := userFromContext(ctx)
	if in.Action != "install" {
		detail, err := client.GetReleaseAsUser(in.Namespace, in.Name, user)
		if err != nil {
			return nil, nil, fmt.Errorf("read release before %s: %w", in.Action, err)
		}
		if operation.Err() != nil {
			return nil, nil, fmt.Errorf("cluster connection changed during release lookup; retry on the intended cluster")
		}
		status := release.Status(detail.Status)
		if status.IsPending() || status == release.StatusUninstalling {
			return nil, nil, fmt.Errorf("release is %s; wait for the active Helm operation, or ask an operator to recover a stranded release before retrying", detail.Status)
		}
		if detail.ManagedByFluxHelmRelease != "" {
			return nil, nil, fmt.Errorf("release is managed by Flux HelmRelease %s; change its source configuration instead", detail.ManagedByFluxHelmRelease)
		}
		if in.Action == "uninstall" {
			bindingBytes, err := json.Marshal(struct {
				Namespace, Name string
				Revision        int
				Updated         any
				Status          string
			}{in.Namespace, in.Name, detail.Revision, detail.Updated, detail.Status})
			if err != nil {
				return nil, nil, err
			}
			binding := string(bindingBytes)
			if in.Confirm != "" {
				confirmed, err := verifyMutationConfirmation(ctx, "uninstall_helm", in.Confirm)
				if err != nil {
					return nil, nil, fmt.Errorf("uninstall confirmation rejected; omit confirm and review a fresh preview")
				}
				if confirmed != binding {
					return nil, nil, fmt.Errorf("release changed since the preview; omit confirm and review a new preview")
				}
			} else {
				token, err := issueMutationConfirmation(ctx, "uninstall_helm", binding)
				if err != nil {
					return nil, nil, err
				}

				audit.Outcome = "preview"
				return toJSONResult(map[string]any{"action": "uninstall", "state": "preview", "namespace": in.Namespace, "name": in.Name, "revision": detail.Revision, "confirm": token, "resources": detail.Resources, "message": fmt.Sprintf("Release status: %s. Review the resources and retained data before calling again with confirm. Helm hooks and resource policies can affect deletion; this is not a complete cascade preview.", detail.Status)})
			}
		}
	}
	if operation.Err() != nil {
		return nil, nil, fmt.Errorf("cluster connection changed; retry on the intended cluster")
	}
	audit.Outcome = "failed_or_unconfirmed"
	var err error
	var upgrade *helm.MCPUpgradeResult
	switch in.Action {
	case "install":
		_, err = client.InstallFreshAsUser(&helm.InstallRequest{ReleaseName: in.Name, Namespace: in.Namespace, ChartName: in.Chart, Repository: in.Repository, Version: in.Version, Values: in.Values}, user)
	case "upgrade":
		upgrade, err = client.UpgradeForMCPAsUser(helm.MCPUpgradeRequest{Namespace: in.Namespace, Name: in.Name, Version: in.Version, Repository: in.Repository, Values: in.Values, ReplaceValues: in.ReplaceValues, DryRun: in.DryRun, ExpectedRevision: in.ExpectedRevision}, user)
	case "rollback":
		err = client.RollbackAsUser(in.Namespace, in.Name, in.Revision, user)
	case "uninstall":
		err = client.UninstallAsUser(in.Namespace, in.Name, user)
	}
	if err != nil {
		var history *helm.ReleaseHistoryError
		if errors.As(err, &history) {
			if history.Status == string(release.StatusFailed) || history.Status == string(release.StatusSuperseded) {
				return nil, nil, fmt.Errorf("%w; inspect get_helm_release include=history,values, then use action=upgrade with reviewed overrides if the stored chart and source are usable; install never recovers existing history", err)
			}
			return nil, nil, fmt.Errorf("%w; use a new release name or ask an operator to recover retained history; upgrade cannot reinstall an uninstalled release", err)
		}
		if in.Action == "upgrade" && in.Version != "" && helm.ChartVersionUnavailable(err) {
			// A wrong version only says it is missing; point at the real choices.
			err = fmt.Errorf("%w; if version %s may not exist, list exact versions with get_helm_release include=versions", err, in.Version)
		}
		if in.DryRun {
			return nil, nil, fmt.Errorf("Helm upgrade preview failed without applying changes: %w; inspect get_helm_release; for source errors ask the Radar configuration owner to configure and confirm the release source", err)
		}
		var preflight *helm.OperationPreflightError
		if errors.As(err, &preflight) {
			return nil, nil, fmt.Errorf("Helm %s refused before applying changes: %w; inspect get_helm_release; for source errors ask the Radar configuration owner to configure and confirm the release source", in.Action, err)
		}
		return nil, nil, fmt.Errorf("Helm %s failed: %w; for source errors ask the Radar configuration owner to configure the source and confirm it for this release; inspect get_helm_release before retrying because the operation may have partially changed the release", in.Action, err)
	}
	if in.DryRun {
		audit.Outcome = "preview"
		return helmUpgradePreviewResult(in, upgrade)
	}
	audit.Outcome = "completed"
	message := "Helm operation completed. Use get_helm_release and wait_for to verify workload readiness; completion does not prove application behavior."
	if in.Action == "uninstall" {
		message = "Helm uninstall completed. Inspect retained resources and hooks; use wait_for condition=deleted for workloads expected to disappear. A missing Helm release alone does not prove all resources or data were deleted."
	}
	result := map[string]any{"action": in.Action, "state": "completed", "namespace": in.Namespace, "name": in.Name, "message": message}
	if upgrade != nil {
		result["chartVersion"], result["revision"], result["baselineRevision"] = upgrade.ChartVersion, upgrade.Revision, upgrade.BaselineRevision
	}
	return toJSONResult(result)
}

func helmUpgradePreviewResult(in manageHelmInput, preview *helm.MCPUpgradeResult) (*mcp.CallToolResult, any, error) {
	result := map[string]any{"action": "upgrade", "state": "preview", "namespace": in.Namespace, "name": in.Name,
		"chartVersion": preview.ChartVersion, "currentRevision": preview.Revision - 1, "baselineRevision": preview.BaselineRevision,
		"valuesRedacted": true, "parseErrors": preview.ParseErrors,
		"message": "Advisory preview only; nothing was applied. Values compare stored overrides after merging, not all chart defaults; hidden changes may be redacted. Resource identities compare release manifests, exclude hooks and are incomplete when parsing or output is truncated; removal does not guarantee deletion (retention policies apply). Rendering uses caller-authorized lookups; hooks, admission, future lookups/random values and readiness are not verified. To execute, set dry_run=false with the same inputs and expected_revision=currentRevision. This precondition is not a lock against other writers."}
	buf := &cappedBuffer{limit: 16 * 1024}
	_, _ = buf.Write([]byte(preview.ValuesDiff))
	addTruncation(result, "valuesDiff", buf)
	for key, refs := range map[string][]helm.ResourceRef{"added": preview.Added, "changed": preview.Changed, "removed": preview.Removed} {
		result[key+"Count"] = len(refs)
		result[key+"Truncated"] = len(refs) > 100
		if len(refs) > 100 {
			refs = refs[:100]
		}
		if refs == nil {
			refs = []helm.ResourceRef{}
		}
		result[key] = refs
	}
	return toJSONResult(result)
}
