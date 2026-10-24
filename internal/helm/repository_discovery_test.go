package helm

import (
	"os"
	"testing"
)

func TestListRepositoriesBeforeFirstRepository(t *testing.T) {
	c := newChartSourceTestClient(t)
	repositories, err := c.ListRepositories()
	if err != nil || len(repositories) != 0 {
		t.Fatalf("fresh machine repositories = %v, %v", repositories, err)
	}
	charts, err := c.SearchCharts("podinfo", false)
	if err != nil || len(charts.Charts) != 0 {
		t.Fatalf("fresh machine charts = %v, %v", charts, err)
	}
	if err := os.WriteFile(c.settings.RepositoryConfig, []byte("repositories: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListRepositories(); err == nil {
		t.Fatal("corrupt repository configuration must remain an error")
	}
	if _, err := c.SearchCharts("podinfo", false); err == nil {
		t.Fatal("corrupt repository configuration must remain a search error")
	}
}
