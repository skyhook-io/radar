package helm

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
)

// ReleasePendingError is returned when a prior install/upgrade left the release
// in a pending-* state. Callers should surface this to the user with a "clean
// up and retry" affordance rather than blindly retrying — Helm itself refuses
// to operate on pending releases to avoid concurrent-write corruption.
type ReleasePendingError struct {
	Name      string
	Namespace string
	Status    string
	Revision  int
}

func (e *ReleasePendingError) Error() string {
	return fmt.Sprintf("release %q in namespace %q is stuck in %s (revision %d)",
		e.Name, e.Namespace, e.Status, e.Revision)
}

// ReleaseExistsError is returned when an install is requested for a release
// that already exists in a healthy deployed state. This is distinct from the
// pending case — caller can offer "upgrade instead" rather than "uninstall".
type ReleaseExistsError struct {
	Name      string
	Namespace string
	Revision  int
}

func (e *ReleaseExistsError) Error() string {
	return fmt.Sprintf("release %q in namespace %q already exists (revision %d)",
		e.Name, e.Namespace, e.Revision)
}

// installMode is what preInstallCheck tells the caller about how to dispatch
// the install. Three modes because Helm's SDK splits recovery semantics by
// prior release status: only Failed/Superseded recover via action.Upgrade
// (which has the deployed-base fallback at upgrade.go:231-233), Uninstalled
// requires action.Install with Replace=true (upgrade rejects it with
// ErrNoDeployedReleases), and a fresh install uses action.Install with no
// replace.
type installMode int

const (
	installFresh   installMode = iota // no prior record
	installReplace                    // prior record is Uninstalled
	installUpgrade                    // prior record is Failed or Superseded
)

// preInstallCheck inspects existing Helm storage for the release name and
// returns:
//   - (installFresh, nil): no record
//   - (installReplace, nil): a prior Uninstalled record (use Install.Replace)
//   - (installUpgrade, nil): a prior Failed/Superseded record (use Upgrade)
//   - (_, *ReleasePendingError): a prior attempt is stuck in pending-* /
//     uninstalling / unrecognized status (fail-closed)
//   - (_, *ReleaseExistsError): the release is currently deployed
//
// Uses Releases.Last because action.History.Run returns the storage driver's
// raw Query output (unsorted, ignores Max), so its hist[0] is non-deterministic.
func preInstallCheck(actionConfig *action.Configuration, name, namespace string) (installMode, error) {
	last, lErr := actionConfig.Releases.Last(name)
	if errors.Is(lErr, driver.ErrReleaseNotFound) {
		return installFresh, nil
	}
	if lErr != nil {
		return installFresh, fmt.Errorf("failed to inspect existing release: %w", lErr)
	}
	switch last.Info.Status {
	case release.StatusPendingInstall, release.StatusPendingUpgrade, release.StatusPendingRollback, release.StatusUninstalling:
		return installFresh, &ReleasePendingError{
			Name: name, Namespace: namespace,
			Status: last.Info.Status.String(), Revision: last.Version,
		}
	case release.StatusDeployed:
		return installFresh, &ReleaseExistsError{
			Name: name, Namespace: namespace, Revision: last.Version,
		}
	case release.StatusFailed, release.StatusSuperseded:
		return installUpgrade, nil
	case release.StatusUninstalled:
		return installReplace, nil
	default:
		// StatusUnknown or any future helm tier: fail-closed. Neither
		// action.Upgrade nor Install.Replace accepts arbitrary statuses
		// (install.go:549 only allows Uninstalled+Failed for Replace;
		// upgrade.go:232 only allows Failed+Superseded for fallback), so
		// silently routing here would yield a confusing helm error.
		log.Printf("[helm] preInstallCheck: unrecognized release status %q for %q/%q, refusing to overwrite", last.Info.Status, namespace, name)
		return installFresh, &ReleasePendingError{
			Name: name, Namespace: namespace,
			Status: last.Info.Status.String(), Revision: last.Version,
		}
	}
}

// runInstallOrUpgrade dispatches to the right Helm action for the install
// mode. Failed/Superseded go through action.Upgrade (with its
// deployed-base fallback); Uninstalled goes through action.Install with
// Replace=true (helm SDK exposes no equivalent fallback on Upgrade — the
// CLI's `helm upgrade --install` is implemented at the CLI layer, not in
// the action; see upgrade.go:49-58).
func runInstallOrUpgrade(actionConfig *action.Configuration, req *InstallRequest, ch *chart.Chart, mode installMode) (*release.Release, error) {
	if mode == installUpgrade {
		upgrade := action.NewUpgrade(actionConfig)
		upgrade.Install = true
		upgrade.Namespace = req.Namespace
		upgrade.Timeout = 120 * time.Second
		upgrade.MaxHistory = 10
		upgrade.Version = req.Version
		// action.Upgrade has no CreateNamespace; reaching this branch implies a
		// prior release record exists, so the namespace was created earlier. If
		// it has been deleted manually since, the user must recreate it.
		return upgrade.Run(req.ReleaseName, ch, req.Values)
	}
	install := action.NewInstall(actionConfig)
	install.ReleaseName = req.ReleaseName
	install.Namespace = req.Namespace
	install.CreateNamespace = req.CreateNamespace
	install.Timeout = 120 * time.Second
	install.Version = req.Version
	install.Replace = mode == installReplace
	return install.Run(ch, req.Values)
}

// rbacPreflightRe matches Helm's wrapped pre-flight RBAC error. Helm formats
// it as: `could not get information about the resource <Kind> "<name>" in
// namespace "<ns>": <gvr> "<name>" is forbidden: User "<u>" cannot <verb>
// resource "<resource>" in API group "<group>" at the cluster scope`
// (or "in the namespace" for namespaced resources).
var rbacPreflightRe = regexp.MustCompile(
	`is forbidden: User "([^"]*)" cannot (\w+) resource "([^"]+)" in API group "([^"]*)"`,
)

// RBACPreflightDetail describes a parsed Helm pre-flight RBAC denial.
type RBACPreflightDetail struct {
	User     string
	Verb     string
	Resource string
	Group    string
}

// classifyHelmRBACError returns parsed detail if the error came from a Helm
// pre-flight existence check that was denied by Kubernetes RBAC.
func classifyHelmRBACError(err error) (*RBACPreflightDetail, bool) {
	if err == nil {
		return nil, false
	}
	m := rbacPreflightRe.FindStringSubmatch(err.Error())
	if m == nil {
		return nil, false
	}
	return &RBACPreflightDetail{User: m[1], Verb: m[2], Resource: m[3], Group: m[4]}, true
}

// InstallErrorClass is the unified mapping of a Helm install error onto a
// user-facing response — same shape used by the JSON HTTP path
// (writeInstallError) and the SSE streaming path (handleInstallStream).
// Code is empty for unclassified errors; callers fall back to a generic 500.
type InstallErrorClass struct {
	Status  int
	Code    string
	Message string
}

// classifyInstallError builds the response shape (status, error_code, message)
// from a Helm install error. Single source of truth so streaming and
// non-streaming endpoints agree on the user-visible message and status.
func classifyInstallError(err error) InstallErrorClass {
	if err == nil {
		return InstallErrorClass{}
	}
	var pending *ReleasePendingError
	if errors.As(err, &pending) {
		return InstallErrorClass{
			Status: http.StatusConflict,
			Code:   "release_pending",
			Message: fmt.Sprintf("a previous install of %q in namespace %q ended in %s — uninstall and retry, or wait for it to finish",
				pending.Name, pending.Namespace, pending.Status),
		}
	}
	var exists *ReleaseExistsError
	if errors.As(err, &exists) {
		return InstallErrorClass{
			Status: http.StatusConflict,
			Code:   "release_exists",
			Message: fmt.Sprintf("release %q already exists in namespace %q (revision %d) — use upgrade",
				exists.Name, exists.Namespace, exists.Revision),
		}
	}
	if rbac, ok := classifyHelmRBACError(err); ok {
		group := rbac.Group
		if group == "" {
			group = "core"
		}
		return InstallErrorClass{
			Status: http.StatusForbidden,
			Code:   "rbac_preflight",
			Message: fmt.Sprintf("Radar identity %q is missing %s on %s.%s — see the in-cluster RBAC docs to expand permissions",
				rbac.User, rbac.Verb, rbac.Resource, group),
		}
	}
	if IsForbiddenError(err) {
		return InstallErrorClass{
			Status:  http.StatusForbidden,
			Message: "insufficient permissions to install Helm release",
		}
	}
	return InstallErrorClass{
		Status:  http.StatusInternalServerError,
		Message: err.Error(),
	}
}

// writeInstallError maps a Helm install error onto an HTTP response with a
// stable error_code the frontend can branch on.
func writeInstallError(w http.ResponseWriter, err error) {
	cls := classifyInstallError(err)
	if cls.Code != "" {
		writeErrorCode(w, cls.Status, cls.Code, cls.Message)
		return
	}
	writeError(w, cls.Status, cls.Message)
}

// recoveryMode returns a short human-readable label for the non-fresh modes,
// used in operational logs.
func recoveryMode(m installMode) string {
	switch m {
	case installReplace:
		return "install --replace"
	case installUpgrade:
		return "upgrade --install"
	}
	return "fresh"
}

// installStreamErrorEvent builds the SSE error envelope from a Helm install
// error, using the same classifier as writeInstallError so the streaming
// install endpoint surfaces the same friendly messages and error codes the
// JSON endpoint does.
func installStreamErrorEvent(err error) map[string]any {
	cls := classifyInstallError(err)
	event := map[string]any{
		"type":    "error",
		"message": cls.Message,
	}
	if cls.Code != "" {
		event["error_code"] = cls.Code
	}
	return event
}
