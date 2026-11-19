package helm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// ErrObjectsUnavailable means objects can't answer yet: the cache isn't ready,
// or a read found nothing while what it couldn't reach may still settle. A
// retry can succeed.
var ErrObjectsUnavailable = errors.New("resource cache isn't ready; try again")

// ErrAccessCheckFailed means Radar couldn't work out which namespaces the
// caller may read, so it attempted no read. That is not a denial, and a retry
// can succeed.
var ErrAccessCheckFailed = errors.New("couldn't check your namespace access; try again")

// ReleaseInventory is the Helm list as far as the caller's access reaches:
// releases read from Helm's storage, plus, where that read was denied,
// releases observed from the stamps on deployed objects.
type ReleaseInventory struct {
	Releases []HelmRelease     `json:"releases"`
	Observed []ObservedRelease `json:"observed"`
	// StorageDenied says where the caller can't read Helm's storage, among the
	// namespaces it asked about. Absent when nothing was denied, the only case
	// where the list comes entirely from Helm.
	StorageDenied *resourceapi.NameSet `json:"storageDenied,omitempty"`
	// ObjectCoverage says what the object read reached. Present exactly when
	// that read ran.
	ObjectCoverage *resourceapi.SearchCoverage `json:"objectCoverage,omitempty"`
}

// InventoryRequest is one caller's inventory read.
type InventoryRequest struct {
	// StorageNamespaces is where to read Helm's storage, resolved as for
	// ListReleasesAcrossNamespaces: nil is cluster-wide, and non-nil empty is
	// nowhere. It can be narrower than what the caller can see, since it's
	// narrowed to where they may list Secrets.
	StorageNamespaces []string
	// StorageCheckFailed means a Secrets permission check behind
	// StorageNamespaces went unanswered, so a namespace missing from it may not
	// be denied. The read then fails as retryable rather than reporting a
	// denial.
	StorageCheckFailed bool
	// StorageSkipped are namespaces the storage scope left out because the
	// reading identity can't list Secrets there. Without a user they are,
	// with denials, the only namespaces left unread.
	StorageSkipped []string
	User           *pkgauth.User
	// Scope is the caller's view of cached objects. Nil means the cache isn't
	// available.
	Scope *ObjectScope
}

// ReadInventory reads what ListReleasesAcrossNamespaces reads and fills in the
// rest from stamps. Any failure other than a denial of the caller's own read
// fails it as it fails that list, so a broken connection never reads as a
// permissions gap. A caller who can read all the storage it asked about costs
// no object read.
func (c *Client) ReadInventory(ctx context.Context, req InventoryRequest) (*ReleaseInventory, error) {
	inventory := &ReleaseInventory{Releases: []HelmRelease{}, Observed: []ObservedRelease{}}
	if req.Scope != nil && req.Scope.Requested != nil && len(req.Scope.Requested) == 0 {
		return inventory, nil
	}
	var read, denied []string
	if req.StorageNamespaces != nil && len(req.StorageNamespaces) == 0 && req.Scope != nil && req.Scope.Access.NamespaceCheckFailed {
		return nil, ErrAccessCheckFailed
	}
	if req.StorageCheckFailed {
		return nil, ErrAccessCheckFailed
	}
	if req.StorageNamespaces == nil || len(req.StorageNamespaces) > 0 {
		client, err := c.storageClient(req.User)
		if err != nil {
			return nil, err
		}
		var access StatusAccess
		if req.Scope != nil {
			access = req.Scope
		}
		inventory.Releases, read, denied, err = listReleasesRecordingAccess(ctx, client, k8s.GetContextName(), req.StorageNamespaces, access)
		if err != nil {
			return nil, err
		}
		if req.StorageNamespaces == nil && len(denied) == 0 {
			return inventory, nil
		}
	}
	inventory.StorageDenied = unreadStorage(req, read, denied)
	if inventory.StorageDenied == nil {
		return inventory, nil
	}
	if req.Scope == nil {
		return nil, ErrObjectsUnavailable
	}
	scan, err := scanObservedReleases(ctx, req.Scope)
	if err != nil {
		return nil, err
	}
	inventory.ObjectCoverage = &scan.coverage
	inventory.Observed = observedOutsideStorage(scan, read, req.Scope.Requested, inventory.Releases)
	// Health reads the current cache, so this has to come after it. It also
	// keeps a storage read from one cluster from pairing with objects of another.
	if !req.Scope.Provider.Current() {
		return nil, ErrConnectionChanged
	}
	return inventory, nil
}

// Summary says in words what a partial inventory covers, for readers that get
// no callout.
func (i *ReleaseInventory) Summary() string {
	where := "in every namespace"
	if i.StorageDenied != nil && i.StorageDenied.Extent == "" {
		where = fmt.Sprintf("in %d namespaces", i.StorageDenied.Count)
		if len(i.StorageDenied.Names) > 0 {
			where = "in " + strings.Join(i.StorageDenied.Names, ", ")
		}
	} else if i.StorageDenied != nil && i.StorageDenied.Extent == resourceapi.ExtentUnknown {
		where = "in some namespaces"
	}
	summary := fmt.Sprintf("Partial: %d releases read from Helm and %d observed. Helm's release Secrets can't be read %s, so observed rows come from the meta.helm.sh/release-name and meta.helm.sh/release-namespace annotations and the helm.sh/chart and app.kubernetes.io/version labels on objects you can read. Their status, revision, last update, history, values, manifest, notes and hooks are unknown, so don't assume they are deployed or healthy. get_helm_release returns one of them, built the same way, with its objects; inspect those with list_resources or search. Absence proves nothing: a release that failed before creating any object, or whose objects you can't read, isn't listed, and objects kept after an uninstall can still appear. Anyone who can edit an object can set these annotations, so a row is a claim, not proof that a release exists. Helm's records need get and list on Secrets for the identity Radar reads as here, which also exposes every Secret in those namespaces.", len(i.Releases), len(i.Observed), where)
	return summary + objectGaps(i.ObjectCoverage, "list_helm_releases")
}

// Summary says in words what an observed release is and isn't, for
// get_helm_release, whose readers get no callout.
func (d *ObservedReleaseDetail) Summary() string {
	summary := fmt.Sprintf("Observed: Helm's release Secrets can't be read here, so this release comes from the meta.helm.sh/release-name and meta.helm.sh/release-namespace annotations and the helm.sh/chart and app.kubernetes.io/version labels on the %d objects you can read that carry them, which can be fewer than it has. Its status, revision, last update, history, values, manifest, notes and hooks are unknown, so don't assume it is deployed or healthy. Anyone who can edit an object can set these annotations, so this is a claim, not proof that the release exists.", d.ResourceCount)
	if len(d.FluxHistory) > 0 {
		summary += " fluxHistory is the Flux HelmRelease's record of what it installed, not Helm's revisions."
	}
	return summary + objectGaps(&d.ObjectCoverage, "get_helm_release")
}

// objectGaps says in words what the object read didn't reach, naming the tool
// to call again when the read may settle.
func objectGaps(coverage *resourceapi.SearchCoverage, tool string) string {
	if coverage == nil || len(coverage.Gaps) == 0 {
		return ""
	}
	var codes []string
	for _, gap := range coverage.Gaps {
		codes = append(codes, string(gap.Code))
	}
	slices.Sort(codes)
	gaps := fmt.Sprintf(" Some objects weren't read (%s), so more may be missing.", strings.Join(slices.Compact(codes), ", "))
	if coverageMaySettle(coverage.Gaps) {
		gaps += " Call " + tool + " again shortly."
	}
	return gaps
}

// RequestedNamespaces is what a caller narrowed a read to: the namespaces it
// named, within Radar's --namespace-scope pin if there is one. Nil means no
// narrowing. Non-nil empty means it named nothing Radar covers, so there is
// nothing to answer, which isn't a denial.
func RequestedNamespaces(named []string, pin *string) []string {
	if pin == nil {
		return named
	}
	if *pin == "" || (named != nil && !slices.Contains(named, *pin)) {
		return []string{}
	}
	return []string{*pin}
}

// listReleasesRecordingAccess is listReleasesAcrossNamespacesWithClient that
// also says which scopes were read and which the caller was denied. A complete
// denial is a result here, not an error.
func listReleasesRecordingAccess(ctx context.Context, client kubernetes.Interface, contextName string, namespaces []string, access StatusAccess) (rows []HelmRelease, read, denied []string, err error) {
	rows, err = listReleaseRows(ctx, contextName, namespaces, true, access, false, ReleaseReadDeniedForCaller, func(ctx context.Context, namespace string) (*helmReleaseStorageSnapshot, error) {
		snapshot, readErr := helmReleaseStorageSnapshotWithClient(ctx, client, namespace)
		switch {
		case readErr == nil:
			read = append(read, namespace)
		case ReleaseReadDeniedForCaller(readErr):
			denied = append(denied, namespace)
		}
		return snapshot, readErr
	})
	if err != nil {
		if !ReleaseReadDeniedForCaller(err) {
			return nil, nil, nil, err
		}
		return []HelmRelease{}, read, denied, nil
	}
	return rows, read, denied, nil
}

// unreadStorage is the part of what the caller asked about whose storage it
// didn't read, or nil if there is none. The storage scope can be narrower than
// the request: it leaves out namespaces where the caller may not list Secrets,
// so the storage read never reports those as denied.
func unreadStorage(req InventoryRequest, read, denied []string) *resourceapi.NameSet {
	if len(read) == 0 && (len(denied) == 0 || slices.Contains(denied, "")) {
		return resourceapi.AllNames()
	}
	var wanted []string
	if req.Scope != nil {
		wanted = req.Scope.Requested
		if wanted == nil {
			wanted = req.Scope.Access.Selection.Namespaces
		}
	}
	if wanted == nil {
		// Without a user the storage scope is what Radar's own identity
		// reaches, so only a denial or a namespace it skipped is unread.
		if req.User == nil || req.Scope == nil {
			return namesOrNil(UnreadStorageNamespaces(req.StorageSkipped, denied))
		}
		wanted = k8s.AllNamespaceNames()
		if len(wanted) == 0 {
			return resourceapi.UnknownNames()
		}
	}
	unread := []string{}
	for _, ns := range wanted {
		if !slices.Contains(read, ns) {
			unread = append(unread, ns)
		}
	}
	return namesOrNil(unread)
}

// UnreadStorageNamespaces joins namespaces a storage scope left out with ones
// whose read was refused, ignoring the cluster-wide scope (""), sorted.
func UnreadStorageNamespaces(left, refused []string) []string {
	out := slices.Clone(left)
	for _, ns := range refused {
		if ns != "" {
			out = append(out, ns)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func namesOrNil(namespaces []string) *resourceapi.NameSet {
	if len(namespaces) == 0 {
		return nil
	}
	return resourceapi.Names(namespaces, resourceapi.RESTSetLimit)
}

// observedOutsideStorage builds rows for releases the storage read couldn't
// have answered. A release in a namespace whose storage was read isn't one: if
// it were there the read returned it, and if not, Helm doesn't have it and its
// objects are leftovers, as Helm's own list would agree. A Flux release stored
// elsewhere is judged by where it's stored. Requested filters on the release's
// namespace, after the scan, so a release keeps the objects it placed in other
// namespaces and its cluster-scoped ones. A release Helm already returned is
// left out however it was stored: without its HelmRelease, the scan can't tell.
// storageWasRead reports whether the storage read covered everywhere the
// release could be stored: where each readable Flux HelmRelease deploying it
// stores it, or its own namespace when none does. Two HelmReleases can deploy
// one release from different storage namespaces, and a row credited to
// neither still has to be judged by both.
func storageWasRead(key releaseKey, flux []fluxEvidence, read []string) bool {
	stored := false
	for _, f := range flux {
		if f.releaseNamespace != key.namespace || f.releaseName != key.name {
			continue
		}
		stored = true
		if !slices.Contains(read, f.storageNamespace) {
			return false
		}
	}
	return stored || slices.Contains(read, key.namespace)
}

func observedOutsideStorage(scan *stampScan, read, requested []string, releases []HelmRelease) []ObservedRelease {
	known := make(map[releaseKey]bool, len(releases))
	for _, release := range releases {
		known[releaseKey{release.Namespace, release.Name}] = true
	}
	rows := []ObservedRelease{}
	partial := scan.coverage.Status != resourceapi.CoverageComplete
	for key, objects := range groupStamped(scan.stamped) {
		row := observedRow(key, objects, scan.flux)
		if known[key] || storageWasRead(key, scan.flux, read) || (requested != nil && !slices.Contains(requested, row.Namespace)) {
			continue
		}
		addObservedHealth(&row, objects, partial)
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Namespace != rows[j].Namespace {
			return rows[i].Namespace < rows[j].Namespace
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}
