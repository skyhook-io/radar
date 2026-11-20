package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/Masterminds/semver/v3"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
)

type manageHelmInput struct {
	Action           string         `json:"action" jsonschema:"install, upgrade, rollback, or uninstall"`
	Namespace        string         `json:"namespace" jsonschema:"release storage namespace (target namespace for install)"`
	Name             string         `json:"name" jsonschema:"Helm release name"`
	Chart            string         `json:"chart,omitempty" jsonschema:"chart name; required for install"`
	Repository       string         `json:"repository,omitempty" jsonschema:"install: required configured repository name, URL or OCI prefix. upgrade: optional configured repository name; must match the release's confirmed saved source"`
	Version          string         `json:"version,omitempty" jsonschema:"exact chart version; required for install (choices: get_helm_chart); upgrade choices: get_helm_release include=versions; omission keeps the stored version only when latest and deployed history agree"`
	DryRun           bool           `json:"dry_run,omitempty" jsonschema:"upgrade or rollback: non-mutating advisory preview (upgrade: same merge and caller lookups; rollback: stored diff, live takeover/retention); default false executes"`
	ExpectedRevision int            `json:"expected_revision,omitempty" jsonschema:"upgrade or rollback: refuse if latest release revision differs; use revision from get_helm_release or currentRevision from preview; not an atomic lock"`
	Values           map[string]any `json:"values,omitempty" jsonschema:"install values or upgrade overrides; merge uses the deployed baseline, not necessarily the latest values read; replace_values=true resets overrides; no redacted output or literal credentials"`
	ReplaceValues    bool           `json:"replace_values,omitempty" jsonschema:"upgrade only: explicitly replace ALL stored overrides with values, including an empty object to reset; default false merges maps and refuses hidden-credential container replacement; true drops omitted overrides, including hidden credentials"`
	Revision         int            `json:"revision,omitempty" jsonschema:"positive target revision; required for rollback; inspect get_helm_release include=history first"`
	Confirm          string         `json:"confirm,omitempty" jsonschema:"uninstall only: omit to preview release resources; pass the reviewed preview token to uninstall (single-use, expires after 5 minutes)"`
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
			return fmt.Errorf("install requires chart, repository and an exact version; list versions with get_helm_chart")
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
	if in.ExpectedRevision < 0 || (in.Action != "upgrade" && in.Action != "rollback" && (in.DryRun || in.ExpectedRevision != 0)) {
		return fmt.Errorf("dry_run and a positive expected_revision are only valid for upgrade or rollback")
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

// redactedError masks credential-like text in the error an agent reads (Helm
// render and API errors can echo chart text or values) and keeps the chain so
// typed errors still render as structured results. mask, when set, also masks
// known values; it defaults to pattern masking.
type redactedError struct {
	err  error
	mask func(string) string
}

func (e redactedError) Error() string {
	if e.mask != nil {
		return e.mask(e.err.Error())
	}
	return aicontext.RedactSecrets(e.err.Error())
}
func (e redactedError) Unwrap() error { return e.err }

func handleManageHelm(ctx context.Context, req *mcp.CallToolRequest, in manageHelmInput) (*mcp.CallToolResult, any, error) {
	result, out, err := manageHelm(ctx, req, in)
	if err == nil {
		return result, out, nil
	}
	mask := errorMask(in, err)
	// readErrorResult serializes a readError's own message, so redact inside it.
	var re *readError
	if errors.As(err, &re) {
		redacted := *re
		redacted.err = errors.New(mask(re.err.Error()))
		return result, out, &redacted
	}
	return result, out, redactedError{err: err, mask: mask}
}

// errorMask masks the caller's own sensitive values and those err carries from
// the release's revisions, then credential patterns.
func errorMask(in manageHelmInput, err error) func(string) string {
	secrets := map[string]bool{}
	collectSensitiveValues(in.Values, secrets)
	var known *knownValuesError
	if errors.As(err, &known) {
		for secret := range known.secrets {
			secrets[secret] = true
		}
	}
	return func(text string) string { return aicontext.RedactSecrets(maskKnownValues(text, secrets)) }
}

// knownValuesError carries the sensitive values stored in the release's
// revisions, so the error an agent reads can mask them: a template's fail or
// required, or an API validation error, can echo a stored password verbatim,
// which pattern masking misses.
type knownValuesError struct {
	err     error
	secrets map[string]bool
}

func (e *knownValuesError) Error() string { return e.err.Error() }
func (e *knownValuesError) Unwrap() error { return e.err }

// refuseFluxManagedRelease keeps ownership protection independent of whether
// the caller may read the controller's namespace and name.
func refuseFluxManagedRelease(detail *helm.HelmReleaseDetail) error {
	if detail.ManagedByFluxHelmRelease != "" {
		return fmt.Errorf("release is managed by Flux HelmRelease %s; change that HelmRelease's source configuration instead", detail.ManagedByFluxHelmRelease)
	}
	if detail.ManagedByFlux {
		return fmt.Errorf("release is managed by Flux, so Radar won't change it with Helm. The owning HelmRelease's namespace is unreadable or access couldn't be checked; change the release through Flux")
	}
	return nil
}

// storedSensitiveValues reads every stored revision's values as the caller,
// through the client the call is pinned to, and returns their sensitive values.
func storedSensitiveValues(client *helm.Client, in manageHelmInput, user *pkgauth.User) map[string]bool {
	secrets := map[string]bool{}
	if detail, readErr := client.GetReleaseAsUser(in.Namespace, in.Name, user, nil); readErr == nil {
		for _, revision := range detail.History {
			if v, readErr := client.GetValuesRevisionAsUser(in.Namespace, in.Name, true, revision.Revision, user); readErr == nil {
				collectSensitiveValues(v.UserSupplied, secrets)
				collectSensitiveValues(v.Computed, secrets)
			}
		}
	}
	return secrets
}

// minMaskedValueLength skips values so short that masking them would mangle
// ordinary words in the message.
const minMaskedValueLength = 4

// maskKnownValues replaces each known value in one pass, longest first, so a
// replacement never matches inside an earlier one.
func maskKnownValues(text string, secrets map[string]bool) string {
	forms := map[string]bool{}
	for secret := range secrets {
		if len(secret) < minMaskedValueLength {
			continue
		}
		forms[secret] = true
		// Kubernetes validation errors quote values with %q, and JSON bodies
		// escape them, so a value with quotes, backslashes or newlines appears
		// escaped in the message.
		forms[strings.Trim(strconv.Quote(secret), `"`)] = true
		if encoded, err := json.Marshal(secret); err == nil {
			forms[strings.Trim(string(encoded), `"`)] = true
		}
	}
	ordered := make([]string, 0, len(forms))
	for form := range forms {
		ordered = append(ordered, form)
	}
	if len(ordered) == 0 {
		return text
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	pairs := make([]string, 0, 2*len(ordered))
	for _, secret := range ordered {
		pairs = append(pairs, secret, "[REDACTED]")
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

func manageHelm(ctx context.Context, _ *mcp.CallToolRequest, in manageHelmInput) (_ *mcp.CallToolResult, _ any, err error) {
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
		detail, err := client.GetReleaseAsUser(in.Namespace, in.Name, user, mcpHelmObjectScope(ctx, ""))
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
		if err := refuseFluxManagedRelease(detail); err != nil {
			return nil, nil, err
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
				// Helm stores the uninstalling status when it records the first hook run
				// (or after hooks, with none). Until then, or if that write fails, the
				// release still reads deployed and a retried confirm would pass the
				// binding and start a second uninstall.
				if !consumeMutationConfirmation(in.Confirm) {
					return nil, nil, fmt.Errorf("this confirmation was already used; an uninstall may still be running, so inspect get_helm_release, then omit confirm for a fresh preview if needed")
				}
			} else {
				preview, err := client.UninstallPreviewAsUser(in.Namespace, in.Name, user)
				if err != nil {
					return nil, nil, fmt.Errorf("read release for the uninstall preview: %w", err)
				}
				if preview.Revision != detail.Revision {
					return nil, nil, fmt.Errorf("release changed while preparing the preview; preview again")
				}
				token, err := issueMutationConfirmation(ctx, "uninstall_helm", binding)
				if err != nil {
					return nil, nil, err
				}
				audit.Outcome = "preview"
				return helmUninstallPreviewResult(in, detail.Status, token, preview)
			}
		}
	}
	if operation.Err() != nil {
		return nil, nil, fmt.Errorf("cluster connection changed; retry on the intended cluster")
	}
	audit.Outcome = "failed_or_unconfirmed"
	// Only Helm's own errors can echo stored values; Radar's refusals above
	// can't, so they skip the extra reads. Uninstall can purge history before
	// it fails, so its values are read first.
	var uninstallSecrets map[string]bool
	if in.Action == "uninstall" {
		uninstallSecrets = storedSensitiveValues(client, in, user)
	}
	defer func() {
		if err == nil {
			return
		}
		secrets := uninstallSecrets
		if secrets == nil {
			secrets = storedSensitiveValues(client, in, user)
		}
		err = &knownValuesError{err: err, secrets: secrets}
	}()
	var upgrade *helm.MCPUpgradeResult
	var rollback *helm.MCPRollbackResult
	switch in.Action {
	case "install":
		_, err = client.InstallFreshAsUser(&helm.InstallRequest{ReleaseName: in.Name, Namespace: in.Namespace, ChartName: in.Chart, Repository: in.Repository, Version: in.Version, Values: in.Values}, user)
	case "upgrade":
		upgrade, err = client.UpgradeForMCPAsUser(helm.MCPUpgradeRequest{Namespace: in.Namespace, Name: in.Name, Version: in.Version, Repository: in.Repository, Values: in.Values, ReplaceValues: in.ReplaceValues, DryRun: in.DryRun, ExpectedRevision: in.ExpectedRevision}, user)
	case "rollback":
		rollback, err = client.RollbackForMCPAsUser(helm.MCPRollbackRequest{Namespace: in.Namespace, Name: in.Name, Revision: in.Revision, ExpectedRevision: in.ExpectedRevision, DryRun: in.DryRun}, user)
	case "uninstall":
		err = client.UninstallAsUser(in.Namespace, in.Name, user)
	}
	if err != nil {
		var preflight *helm.OperationPreflightError
		refused := errors.As(err, &preflight)
		switch {
		case refused:
			audit.Outcome = "refused"
		case in.DryRun:
			audit.Outcome = "failed" // a preview changes nothing
		}
		var history *helm.ReleaseHistoryError
		if errors.As(err, &history) {
			if history.Status == string(release.StatusFailed) || history.Status == string(release.StatusSuperseded) {
				return nil, nil, fmt.Errorf("%w; inspect get_helm_release include=history,values, then use action=upgrade with reviewed overrides if the stored chart and source are usable; install never recovers existing history", err)
			}
			return nil, nil, fmt.Errorf("%w; use a new release name or ask an operator to recover retained history; upgrade cannot reinstall an uninstalled release", err)
		}
		if !in.DryRun && (in.Action == "upgrade" || in.Action == "rollback") && errors.Is(err, driver.ErrReleaseExists) {
			// Helm stores the new revision before touching resources, so losing
			// that race to another writer leaves the release unchanged.
			audit.Outcome = "refused"
			return nil, nil, fmt.Errorf("Helm %s refused before applying changes: another operation stored the next revision of this release first (%w); inspect get_helm_release, then retry only if the change is still wanted", in.Action, err)
		}
		if in.Version != "" && helm.ChartVersionUnavailable(err) {
			// A wrong version only says it is missing; point at the real choices.
			lister := "get_helm_release include=versions"
			if in.Action == "install" {
				lister = "get_helm_chart"
			}
			err = fmt.Errorf("%w; if version %s may not exist, list exact versions with %s", err, in.Version, lister)
		}
		if (refused || in.DryRun) && helm.SourceTimedOut(err) {
			outcome := "refused before applying changes"
			if in.DryRun {
				outcome = "preview failed without applying changes"
			}
			return nil, nil, fmt.Errorf("Helm %s %s: %w; the chart source didn't respond in time (a large index, or a source throttling parallel downloads): retry in a minute", in.Action, outcome, err)
		}
		if in.DryRun && in.Action == "rollback" {
			return nil, nil, fmt.Errorf("Helm rollback preview failed without applying changes: %w; inspect get_helm_release include=history", err)
		}
		if in.DryRun {
			return nil, nil, fmt.Errorf("Helm upgrade preview failed without applying changes: %w; inspect get_helm_release; for source errors ask the Radar configuration owner to configure and confirm the release source", err)
		}
		if refused && in.Action == "rollback" {
			return nil, nil, fmt.Errorf("Helm rollback refused before applying changes: %w; inspect get_helm_release include=history", err)
		}
		if refused {
			return nil, nil, fmt.Errorf("Helm %s refused before applying changes: %w; inspect get_helm_release; for source errors ask the Radar configuration owner to configure and confirm the release source", in.Action, err)
		}
		if in.Action == "uninstall" && uninstallPurgedDespiteError(client, in, user) {
			return nil, nil, fmt.Errorf("Helm uninstall removed the release and purged its history, then reported errors from a later step such as a post-delete hook: %w; the release can't be rolled back; resource deletion may still be in progress (finalizers), so check for leftover release and hook resources and external cleanup", err)
		}
		return nil, nil, fmt.Errorf("Helm %s failed: %w; for source errors ask the Radar configuration owner to configure the source and confirm it for this release; inspect get_helm_release before retrying because the operation may have partially changed the release", in.Action, err)
	}
	if in.DryRun && rollback != nil {
		audit.Outcome = "preview"
		return helmRollbackPreviewResult(in, rollback)
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
	if rollback != nil {
		result["chartVersion"], result["revision"], result["targetRevision"] = rollback.ChartVersion, rollback.Revision, rollback.TargetRevision
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
		addCappedList(result, key, refs)
	}
	return toJSONResult(result)
}

// addCappedList sets key to at most 100 items with keyCount and keyTruncated.
func addCappedList[T any](result map[string]any, key string, items []T) {
	result[key+"Count"] = len(items)
	result[key+"Truncated"] = len(items) > 100
	if len(items) > 100 {
		items = items[:100]
	}
	if items == nil {
		items = []T{}
	}
	result[key] = items
}

func helmRollbackPreviewResult(in manageHelmInput, preview *helm.MCPRollbackResult) (*mcp.CallToolResult, any, error) {
	result := map[string]any{"action": "rollback", "state": "preview", "namespace": in.Namespace, "name": in.Name,
		"targetRevision": preview.TargetRevision, "chartVersion": preview.ChartVersion, "currentRevision": preview.BaselineRevision,
		"valuesRedacted": true, "parseErrors": preview.ParseErrors, "liveChecked": preview.Live.Checked, "rollbackHooks": preview.Hooks,
		"message": "Advisory preview only; nothing was applied. Rollback re-applies the target revision's stored manifest and values without re-rendering; added/changed/removed compare stored manifests and exclude hooks. rollbackHooks run on execute (a failing pre-rollback hook stops the rollback); their contents aren't enumerated. takeovers exist live but aren't recorded as this release's (owner names another release; empty means no Helm owner recorded): rollback overwrites and claims them with no ownership check, so name each takeover to the user and get approval for it specifically before executing; a general go-ahead doesn't cover claiming another release's objects. kept is the part of removed that Helm leaves because the live resource-policy is keep; the rest of removed is deleted, except objects in liveUnknown, which could not be read as you, so their ownership and retention are unknown. To execute, set dry_run=false with the same revision and expected_revision=currentRevision. This precondition is not a lock against other writers."}
	buf := &cappedBuffer{limit: 16 * 1024}
	_, _ = buf.Write([]byte(preview.ValuesDiff))
	addTruncation(result, "valuesDiff", buf)
	for key, refs := range map[string][]helm.ResourceRef{"added": preview.Added, "changed": preview.Changed, "removed": preview.Removed, "kept": preview.Live.Kept, "liveUnknown": preview.Live.Unknown} {
		addCappedList(result, key, refs)
	}
	addCappedList(result, "takeovers", preview.Live.Takeovers)
	return toJSONResult(result)
}

// uninstallPurgedDespiteError reports whether a failed uninstall still removed
// the release: Helm purges history after deleting resources even when a later
// post-delete hook fails, and returns that hook's error.
func uninstallPurgedDespiteError(client *helm.Client, in manageHelmInput, user *pkgauth.User) bool {
	_, err := client.GetReleaseAsUser(in.Namespace, in.Name, user, nil)
	return errors.Is(err, driver.ErrReleaseNotFound)
}

func helmUninstallPreviewResult(in manageHelmInput, status, token string, preview *helm.MCPUninstallPreview) (*mcp.CallToolResult, any, error) {
	message := fmt.Sprintf("Release status: %s. resources are deleted; kept are left in place by helm.sh/resource-policy; deleteHooks run, each removed per its deletePolicies. A failing pre-delete hook stops the uninstall before anything is deleted; a failing post-delete hook doesn't, and history is still purged. Once Helm has requested the resource deletions, the history is purged, so the release can't be rolled back afterwards; deletion itself can still be in progress. Resources created by controllers, PVCs from StatefulSet templates and external data aren't listed; hook contents aren't enumerated. Review, then call again with confirm.", status)
	if preview.HistoryOnly {
		message = "The release is already uninstalled with its history kept. Uninstall only purges the stored history: no hooks run and no resources are deleted. Review, then call again with confirm."
	}
	result := map[string]any{"action": "uninstall", "state": "preview", "namespace": in.Namespace, "name": in.Name, "revision": preview.Revision, "confirm": token,
		"historyOnly": preview.HistoryOnly, "parseErrors": preview.ParseErrors, "deleteHooks": preview.Hooks, "message": message}
	addCappedList(result, "resources", preview.Delete)
	addCappedList(result, "kept", preview.Kept)
	return toJSONResult(result)
}
