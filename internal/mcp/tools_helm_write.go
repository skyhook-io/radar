package mcp

import (
	"context"
	"encoding/json"
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
)

type manageHelmInput struct {
	Action        string         `json:"action" jsonschema:"install, upgrade, rollback, or uninstall"`
	Namespace     string         `json:"namespace" jsonschema:"release storage namespace (target namespace for install)"`
	Name          string         `json:"name" jsonschema:"Helm release name"`
	Chart         string         `json:"chart,omitempty" jsonschema:"chart name; required for install"`
	Repository    string         `json:"repository,omitempty" jsonschema:"install: configured repository name, URL or OCI prefix. upgrade: optional configured repository name; must match the release's confirmed saved source"`
	Version       string         `json:"version,omitempty" jsonschema:"exact chart version; required for install and upgrade"`
	Values        map[string]any `json:"values,omitempty" jsonschema:"install values or upgrade overrides; upgrade merges into stored overrides unless replace_values=true; never reuse redacted output or literal credentials"`
	ReplaceValues bool           `json:"replace_values,omitempty" jsonschema:"upgrade only: explicitly replace ALL stored overrides with values, including an empty object to reset; default false preserves omitted map keys; lists replace; hidden-credential container replacement is refused"`
	Revision      int            `json:"revision,omitempty" jsonschema:"positive target revision; required for rollback; inspect get_helm_release include=history first"`
	Confirm       string         `json:"confirm,omitempty" jsonschema:"uninstall only: omit to preview release resources; pass the reviewed preview token to uninstall (expires after 5 minutes)"`
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
		if in.Version == "" {
			return fmt.Errorf("upgrade requires version; configure and confirm the release's source in Radar first if it is unknown")
		}
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
		if detail.ManagedByFluxHelmRelease != "" {
			return nil, nil, fmt.Errorf("release is managed by Flux HelmRelease %s; change its source configuration instead", detail.ManagedByFluxHelmRelease)
		}
		if in.Action == "uninstall" {
			bindingBytes, err := json.Marshal(struct {
				Namespace, Name string
				Revision        int
				Updated         any
			}{in.Namespace, in.Name, detail.Revision, detail.Updated})
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
				return toJSONResult(map[string]any{"action": "uninstall", "state": "preview", "namespace": in.Namespace, "name": in.Name, "revision": detail.Revision, "confirm": token, "resources": detail.Resources, "message": "Review the resources and any retained data before calling again with the returned confirm token. Helm hooks and resource policies can affect deletion; this is not a complete cascade preview."})
			}
		}
	}
	if operation.Err() != nil {
		return nil, nil, fmt.Errorf("cluster connection changed; retry on the intended cluster")
	}
	audit.Outcome = "failed_or_unconfirmed"
	var err error
	switch in.Action {
	case "install":
		_, err = client.InstallFreshAsUser(&helm.InstallRequest{ReleaseName: in.Name, Namespace: in.Namespace, ChartName: in.Chart, Repository: in.Repository, Version: in.Version, Values: in.Values}, user)
	case "upgrade":
		if in.Values == nil {
			err = client.UpgradeAsUser(in.Namespace, in.Name, in.Version, in.Repository, user)
		} else if !in.ReplaceValues {
			err = client.UpgradeWithOverridesAsUser(in.Namespace, in.Name, in.Version, in.Repository, in.Values, user)
		} else {
			err = client.UpgradeWithValuesProgressAsUser(in.Namespace, in.Name, in.Version, in.Repository, in.Values, user, nil)
		}
	case "rollback":
		err = client.RollbackAsUser(in.Namespace, in.Name, in.Revision, user)
	case "uninstall":
		err = client.UninstallAsUser(in.Namespace, in.Name, user)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("Helm %s failed: %w; for source errors ask the Radar configuration owner to configure the source and confirm it for this release; inspect get_helm_release before retrying because the operation may have partially changed the release", in.Action, err)
	}
	audit.Outcome = "completed"
	message := "Helm operation completed. Use get_helm_release and wait_for to verify workload readiness; completion does not prove application behavior."
	if in.Action == "uninstall" {
		message = "Helm uninstall completed. Inspect retained resources and hooks; use wait_for condition=deleted for workloads expected to disappear. A missing Helm release alone does not prove all resources or data were deleted."
	}
	return toJSONResult(map[string]any{"action": in.Action, "state": "completed", "namespace": in.Namespace, "name": in.Name, "message": message})
}
