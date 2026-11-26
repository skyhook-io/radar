package issuecorrelation

import (
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// The row filter is a second line behind the permission check: a row it
// drops leaves the same rows as no row at all, and status churn never counts
// as evidence.
func TestVisible_HiddenRowIsTheSameAsNoRow(t *testing.T) {
	specConfig := func(kind, apiVersion, ns, name string) issuesapi.RecentChange {
		return issuesapi.RecentChange{Kind: kind, APIVersion: apiVersion, Namespace: ns, Name: name, ChangeCategory: issuesapi.ChangeCategorySpecConfig}
	}
	deploymentsOnly := Access{RowFilter: func(c issuesapi.RecentChange) bool { return c.Kind == "Deployment" }}
	web := specConfig("Deployment", "apps/v1", "shop", "web")
	status := issuesapi.RecentChange{Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web", ChangeCategory: issuesapi.ChangeCategoryRuntimeStatus}

	withHidden := lookbackRun(issuesapi.Issue{}, deploymentsOnly).visible([]issuesapi.RecentChange{web, specConfig("ConfigMap", "v1", "shop", "app-cfg"), status})
	without := lookbackRun(issuesapi.Issue{}, deploymentsOnly).visible([]issuesapi.RecentChange{web, status})
	if len(withHidden) != 1 || len(without) != 1 || !reflect.DeepEqual(withHidden, without) {
		t.Fatalf("hidden row changed the answer: with=%+v without=%+v", withHidden, without)
	}

	// Helm rows are read as the caller already: the row filter skips them.
	helm := issuesapi.RecentChange{Kind: "HelmRelease", Source: HelmChangeSource, Namespace: "shop", Name: "app", ChangeCategory: issuesapi.ChangeCategorySpecConfig}
	if got := lookbackRun(issuesapi.Issue{}, deploymentsOnly).visible([]issuesapi.RecentChange{helm}); len(got) != 1 {
		t.Fatalf("helm row dropped: %+v", got)
	}

	// Auth off (no row filter): passthrough.
	if got := lookbackRun(issuesapi.Issue{}, Access{}).visible([]issuesapi.RecentChange{specConfig("ConfigMap", "v1", "shop", "app-cfg")}); len(got) != 1 {
		t.Fatalf("auth off dropped a row: %+v", got)
	}
}
