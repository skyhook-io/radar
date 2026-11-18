package issuecorrelation

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// When the row filter hides a relevant correlated change, the run must say
// so, so "empty because hidden" is never read as no changes: an issue whose
// consumed ConfigMap rotated must not be stamped with an affirmative
// no_recent_changes claim just because the caller can't read ConfigMaps.
func TestVisible_RBACHiddenIsNotNoChanges(t *testing.T) {
	specConfig := func(kind, apiVersion, ns, name string) issuesapi.RecentChange {
		return issuesapi.RecentChange{Kind: kind, APIVersion: apiVersion, Namespace: ns, Name: name, ChangeCategory: issuesapi.ChangeCategorySpecConfig}
	}
	deploymentsOnly := Access{RowFilter: func(c issuesapi.RecentChange) bool { return c.Kind == "Deployment" }}

	// The only relevant change is an unreadable ConfigMap → empty AND hidden.
	visible, hidden := lookbackRun(issuesapi.Issue{}, deploymentsOnly).visible([]issuesapi.RecentChange{
		specConfig("ConfigMap", "v1", "shop", "app-cfg"),
	})
	if len(visible) != 0 || !hidden {
		t.Fatalf("unreadable ConfigMap: want (0 visible, hidden=true), got (%d, %v)", len(visible), hidden)
	}

	// A readable Deployment change survives, and a status-churn row dropped by
	// the category filter does NOT count as hidden — a genuinely quiet subject
	// must still earn its no_recent_changes marker.
	visible, hidden = lookbackRun(issuesapi.Issue{}, deploymentsOnly).visible([]issuesapi.RecentChange{
		specConfig("Deployment", "apps/v1", "shop", "web"),
		{Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web", ChangeCategory: issuesapi.ChangeCategoryRuntimeStatus},
	})
	if len(visible) != 1 || hidden {
		t.Fatalf("readable Deployment: want (1 visible, hidden=false), got (%d, %v)", len(visible), hidden)
	}

	// Helm rows are read as the caller already: the row filter skips them.
	visible, hidden = lookbackRun(issuesapi.Issue{}, deploymentsOnly).visible([]issuesapi.RecentChange{
		{Kind: "HelmRelease", Source: HelmChangeSource, Namespace: "shop", Name: "app", ChangeCategory: issuesapi.ChangeCategorySpecConfig},
	})
	if len(visible) != 1 || hidden {
		t.Fatalf("helm row: want (1 visible, hidden=false), got (%d, %v)", len(visible), hidden)
	}

	// Auth off (no row filter): passthrough, never hidden.
	visible, hidden = lookbackRun(issuesapi.Issue{}, Access{}).visible([]issuesapi.RecentChange{
		specConfig("ConfigMap", "v1", "shop", "app-cfg"),
	})
	if len(visible) != 1 || hidden {
		t.Fatalf("auth off: want (1 visible, hidden=false), got (%d, %v)", len(visible), hidden)
	}
}
