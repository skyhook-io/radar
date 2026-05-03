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

// preInstallCheck inspects existing Helm storage for the release name and
// returns:
//   - (true, nil): no record, fresh install path
//   - (false, nil): a prior failed/uninstalled record exists; upgrade --install
//     can safely overwrite it
//   - (_, *ReleasePendingError): a prior attempt is stuck in pending-* state
//   - (_, *ReleaseExistsError): the release is currently deployed
func preInstallCheck(actionConfig *action.Configuration, name, namespace string) (fresh bool, err error) {
	historian := action.NewHistory(actionConfig)
	historian.Max = 1
	hist, hErr := historian.Run(name)
	if errors.Is(hErr, driver.ErrReleaseNotFound) {
		return true, nil
	}
	if hErr != nil {
		return false, fmt.Errorf("failed to inspect existing release: %w", hErr)
	}
	if len(hist) == 0 {
		return true, nil
	}
	last := hist[0]
	switch last.Info.Status {
	case release.StatusPendingInstall, release.StatusPendingUpgrade, release.StatusPendingRollback, release.StatusUninstalling:
		return false, &ReleasePendingError{
			Name: name, Namespace: namespace,
			Status: last.Info.Status.String(), Revision: last.Version,
		}
	case release.StatusDeployed:
		return false, &ReleaseExistsError{
			Name: name, Namespace: namespace, Revision: last.Version,
		}
	case release.StatusFailed, release.StatusSuperseded, release.StatusUninstalled, release.StatusUnknown:
		return false, nil
	default:
		log.Printf("[helm] preInstallCheck: unrecognized release status %q for %s/%s, treating as recoverable", last.Info.Status, namespace, name)
		return false, nil
	}
}

// runInstallOrUpgrade performs an idempotent install. When `fresh` is true it
// uses action.Install (the existing path). Otherwise it uses action.Upgrade
// with Install=true, which is the canonical Helm "upgrade --install" semantic
// and overwrites a prior failed/uninstalled record without tripping the
// name-in-use guard.
func runInstallOrUpgrade(actionConfig *action.Configuration, req *InstallRequest, ch *chart.Chart, fresh bool) (*release.Release, error) {
	if fresh {
		install := action.NewInstall(actionConfig)
		install.ReleaseName = req.ReleaseName
		install.Namespace = req.Namespace
		install.CreateNamespace = req.CreateNamespace
		install.Timeout = 120 * time.Second
		install.Version = req.Version
		return install.Run(ch, req.Values)
	}
	upgrade := action.NewUpgrade(actionConfig)
	upgrade.Install = true
	upgrade.Namespace = req.Namespace
	upgrade.Timeout = 120 * time.Second
	upgrade.MaxHistory = 10
	upgrade.Version = req.Version
	// action.Upgrade has no CreateNamespace; the recovery path is only reached
	// when a prior release record exists, which means a previous attempt already
	// got past namespace creation. If the namespace was deleted manually after
	// that, the user must recreate it (Helm install would have done the same).
	return upgrade.Run(req.ReleaseName, ch, req.Values)
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

// classifyInstallErrorCode returns a stable machine-readable code for typed
// install failures. Empty string means "no special code; use generic 500".
func classifyInstallErrorCode(err error) string {
	var pending *ReleasePendingError
	if errors.As(err, &pending) {
		return "release_pending"
	}
	var exists *ReleaseExistsError
	if errors.As(err, &exists) {
		return "release_exists"
	}
	if _, ok := classifyHelmRBACError(err); ok {
		return "rbac_preflight"
	}
	return ""
}

// writeInstallError maps a Helm install error onto an HTTP response. It
// surfaces typed errors (pending release, RBAC pre-flight denial, generic
// forbidden) with stable error_code values the SPA can branch on.
func writeInstallError(w http.ResponseWriter, err error) {
	var pending *ReleasePendingError
	if errors.As(err, &pending) {
		writeErrorCode(w, http.StatusConflict, "release_pending",
			fmt.Sprintf("a previous install of %q in namespace %q ended in %s — uninstall and retry, or wait for it to finish",
				pending.Name, pending.Namespace, pending.Status))
		return
	}
	var exists *ReleaseExistsError
	if errors.As(err, &exists) {
		writeErrorCode(w, http.StatusConflict, "release_exists",
			fmt.Sprintf("release %q already exists in namespace %q (revision %d) — use upgrade",
				exists.Name, exists.Namespace, exists.Revision))
		return
	}
	if rbac, ok := classifyHelmRBACError(err); ok {
		group := rbac.Group
		if group == "" {
			group = "core"
		}
		writeErrorCode(w, http.StatusForbidden, "rbac_preflight",
			fmt.Sprintf("Radar identity %q is missing %s on %s.%s — see the in-cluster RBAC docs to expand permissions",
				rbac.User, rbac.Verb, rbac.Resource, group))
		return
	}
	if IsForbiddenError(err) {
		writeError(w, http.StatusForbidden, "insufficient permissions to install Helm release")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
