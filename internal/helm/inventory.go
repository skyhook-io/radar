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

// ErrObjectsUnavailable means the object cache isn't ready, so the releases
// the caller can't read from Helm's storage can't be observed either.
var ErrObjectsUnavailable = errors.New("resource cache not available")

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
	User              *pkgauth.User
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
	var read, denied []string
	if req.StorageNamespaces == nil || len(req.StorageNamespaces) > 0 {
		client, err := c.storageClient(req.User)
		if err != nil {
			return nil, err
		}
		inventory.Releases, read, denied, err = listReleasesRecordingAccess(ctx, client, k8s.GetContextName(), req.StorageNamespaces)
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
	inventory.Observed = observedOutsideStorage(scan, read, req.Scope.Requested)
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
	return fmt.Sprintf("Partial: %d releases read from Helm and %d observed. Helm's release Secrets can't be read %s, so observed rows come from the meta.helm.sh/release-name and meta.helm.sh/release-namespace annotations and the helm.sh/chart and app.kubernetes.io/version labels on objects you can read. Their status, revision, last update, history, values, manifest, notes and hooks are unknown, so don't assume they are deployed or healthy. Absence proves nothing: a release that failed before creating any object, or whose objects you can't read, isn't listed, and objects kept after an uninstall can still appear. A cluster admin can grant get and list on Secrets for the full view.", len(i.Releases), len(i.Observed), where)
}

// listReleasesRecordingAccess is listReleasesAcrossNamespacesWithClient that
// also says which scopes were read and which the caller was denied. A complete
// denial is a result here, not an error.
func listReleasesRecordingAccess(ctx context.Context, client kubernetes.Interface, contextName string, namespaces []string) (rows []HelmRelease, read, denied []string, err error) {
	rows, err = listReleaseRows(ctx, contextName, namespaces, true, false, ReleaseReadDeniedForCaller, func(ctx context.Context, namespace string) (*helmReleaseStorageSnapshot, error) {
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
		// Without a user the storage scope already is what Radar's own
		// identity reaches, so only a denial leaves a namespace unread.
		if req.User == nil || req.Scope == nil {
			return namesOrNil(denied)
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
// namespaces and its cluster-scoped ones.
func observedOutsideStorage(scan *stampScan, read, requested []string) []ObservedRelease {
	rows := []ObservedRelease{}
	partial := scan.coverage.Status != resourceapi.CoverageComplete
	for key, objects := range groupStamped(scan.stamped) {
		row := observedRow(key, objects, scan.flux)
		storage := row.StorageNamespace
		if storage == "" {
			storage = row.Namespace
		}
		if slices.Contains(read, storage) || (requested != nil && !slices.Contains(requested, row.Namespace)) {
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
