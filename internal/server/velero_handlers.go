package server

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

const veleroGroup = "velero.io"

// VeleroStoredBackup is one Backup held in a storage location.
type VeleroStoredBackup struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Phase     string `json:"phase,omitempty"`
	// Expiration is when Velero will delete this backup. A location full of
	// backups that all expire next week is a different answer to "what can I
	// restore" than one that keeps them for a year.
	Expiration string `json:"expiration,omitempty"`
	// Completed is when the backup finished — the point in time a restore from
	// it would return to.
	Completed string `json:"completed,omitempty"`
}

// maxStoredBackupsListed bounds the list, not the counts. Velero keeps every
// backup until its TTL expires, so an hourly schedule with a long retention
// leaves thousands in one location — and this response backs a panel that
// renders one chip each. Same split the policy coverage lookup makes: the counts
// describe the location, the list is a page of it.
const maxStoredBackupsListed = 200

// VeleroStoredBackupsResponse lists what one storage location holds.
type VeleroStoredBackupsResponse struct {
	// Newest first, and capped at maxStoredBackupsListed. Truncated says so.
	Backups []VeleroStoredBackup `json:"backups"`
	// Stored is how many this location holds in total, counted before the cap —
	// so a reader can tell a location holding 12 from one holding 12,000.
	Stored int `json:"stored"`
	// Truncated reports that the list above is a page. Without it a capped list
	// reads as the whole location.
	Truncated bool `json:"truncated,omitempty"`
	// Restorable counts the backups that reached Completed AND have not passed
	// their expiration.
	//
	// Expiry is not the same as unrestorable — Velero's restore controller does
	// not check it, so an aged-out backup the collector has not reached yet
	// would still restore. It is excluded because this number answers "what can
	// I plan a recovery around", and a backup Velero intends to delete is not
	// that. The panel reading it says so in those terms rather than claiming
	// the data is already gone.
	//
	// Counted over the whole location, like Stored and unlike Backups. A reader
	// recomputing it from the capped list would undercount every location big
	// enough to be capped.
	Restorable int `json:"restorable"`
	// Expired counts the Completed backups that have aged out, for the same
	// reason and over the same scope.
	Expired int `json:"expired"`
}

// handleVeleroStoredBackups returns the Backups held in one BackupStorageLocation.
//
//	GET /api/velero/backupstoragelocations/{namespace}/{name}/backups
//
// The inverse of the field a Backup carries. A storage location page that cannot
// say what depends on it leaves "this location is Unavailable" as a fact with no
// consequence attached, when the consequence — these are the backups you cannot
// restore from right now — is the reason anyone opened the page.
//
// Server-side, gated on listing Backups, for the same reason every other reverse
// lookup here is: the answer's scope belongs to permission, not to whichever
// namespaces the reader happens to be viewing.
func (s *Server) handleVeleroStoredBackups(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if namespace == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "storage location namespace and name are required")
		return
	}
	if !s.canRead(r, veleroGroup, "backups", namespace, "list") {
		s.writeError(w, http.StatusForbidden, "no access to Velero backups")
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	// Backups live in the same namespace as the location that holds them —
	// Velero's own installation namespace.
	items, err := listDynamicSynced(r.Context(), cache, "Backup", veleroGroup, namespace)
	switch {
	case err == nil:
	case errors.Is(err, k8s.ErrUnknownDynamicKind):
		// No Velero on this cluster, so nothing is stored anywhere.
		s.writeJSON(w, VeleroStoredBackupsResponse{Backups: []VeleroStoredBackup{}})
		return
	case errors.Is(err, integration.ErrDynamicNotSynced):
		s.writeError(w, http.StatusServiceUnavailable, "backups are still loading")
		return
	default:
		log.Printf("[velero] Failed to list Backups for storage location %s/%s: %v",
			sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "could not read Velero backups")
		return
	}

	// Which location is the default one, read rather than assumed. A failure to
	// list them is not fatal here: it only costs the fallback, and backups that
	// name their location explicitly are unaffected.
	defaultName := ""
	if locs, lerr := listDynamicSynced(r.Context(), cache, "BackupStorageLocation", veleroGroup, namespace); lerr == nil {
		defaultName = veleroDefaultLocationName(locs)
	}

	resp := VeleroStoredBackupsResponse{Backups: []VeleroStoredBackup{}}
	for _, u := range items {
		if u == nil || veleroBackupLocation(u, defaultName) != name {
			continue
		}
		b := VeleroStoredBackup{Namespace: u.GetNamespace(), Name: u.GetName()}
		b.Phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
		b.Expiration, _, _ = unstructured.NestedString(u.Object, "status", "expiration")
		b.Completed, _, _ = unstructured.NestedString(u.Object, "status", "completionTimestamp")
		if b.Phase == "Completed" {
			if veleroExpired(b.Expiration) {
				resp.Expired++
			} else {
				resp.Restorable++
			}
		}
		resp.Backups = append(resp.Backups, b)
	}
	// Newest first: the question is almost always "what is the most recent thing
	// I can restore from". The cap below depends on this order — the most recent
	// restorable point is derived from this list, so the newest entries must be
	// the ones that survive truncation.
	sort.Slice(resp.Backups, func(i, j int) bool {
		if resp.Backups[i].Completed != resp.Backups[j].Completed {
			return resp.Backups[i].Completed > resp.Backups[j].Completed
		}
		return resp.Backups[i].Name < resp.Backups[j].Name
	})
	resp.Stored = len(resp.Backups)
	if len(resp.Backups) > maxStoredBackupsListed {
		resp.Backups = resp.Backups[:maxStoredBackupsListed]
		resp.Truncated = true
	}
	s.writeJSON(w, resp)
}

// veleroBackupLocation is the storage location a Backup names, applying Velero's
// default.
//
// An unset spec.storageLocation means "the location marked default", which is a
// flag on the location (spec.default) and not a name. Assuming the literal name
// "default" is right on most installs and silently wrong on any that renamed it
// — and being wrong here does not look like an error, it looks like a location
// holding nothing, which is the sentence someone reads before deciding they
// cannot restore. So the flag is what decides, and the name is only the
// fallback for when no location claims it.
func veleroBackupLocation(u *unstructured.Unstructured, defaultLocation string) string {
	if loc, ok, _ := unstructured.NestedString(u.Object, "spec", "storageLocation"); ok && loc != "" {
		return loc
	}
	if defaultLocation != "" {
		return defaultLocation
	}
	return "default"
}

// veleroDefaultLocationName returns the name of the location flagged
// spec.default, or "" when none is. Velero permits at most one.
func veleroDefaultLocationName(locations []*unstructured.Unstructured) string {
	for _, l := range locations {
		if l == nil {
			continue
		}
		if isDefault, ok, _ := unstructured.NestedBool(l.Object, "spec", "default"); ok && isDefault {
			return l.GetName()
		}
	}
	return ""
}

// veleroExpired reports whether Velero's own expiration has passed. Same rule
// the storage-location panel applies client-side; keeping the API's count on a
// different rule would make two numbers describing the same thing disagree.
func veleroExpired(expiration string) bool {
	if expiration == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339, expiration)
	if err != nil {
		return false
	}
	return at.Before(time.Now())
}
