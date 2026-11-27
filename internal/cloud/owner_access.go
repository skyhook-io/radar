package cloud

import (
	"os"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/pkg/auth"
)

var ownerAccessIDs = []string{"argocd", "cnpg", "velero", "velero-download"}

// OwnerAccessConfigured reports whether this installation advertises managed grants.
func OwnerAccessConfigured() bool {
	_, configured := os.LookupEnv("RADAR_CLOUD_OWNER_ACCESS")
	return configured
}

// OwnerAccess returns the managed grants the cluster installation approved.
func OwnerAccess() []string {
	approved := make([]string, 0, len(ownerAccessIDs))
	if !Mode() {
		return approved
	}
	configured := strings.Split(os.Getenv("RADAR_CLOUD_OWNER_ACCESS"), ",")
	for _, id := range ownerAccessIDs {
		if slices.Contains(configured, id) {
			approved = append(approved, id)
		}
	}
	return approved
}

// FilterOwnerAccess accepts managed groups only for an Owner on the trusted
// impersonation path and within the cluster's approved grants.
func FilterOwnerAccess(groups []string, cloudProxy bool) []string {
	owner := cloudProxy && (slices.Contains(groups, "radar:owner") || slices.Contains(groups, "cloud:owner"))
	approved := OwnerAccess()
	return slices.DeleteFunc(slices.Clone(groups), func(group string) bool {
		id, managed := strings.CutPrefix(group, auth.OwnerAccessGroupPrefix)
		return managed && (!owner || !slices.Contains(approved, id))
	})
}
