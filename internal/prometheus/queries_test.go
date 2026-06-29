package prometheus

import (
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
)

func TestMemoryQueriesDedupeScrapeJobsBeforeSumming(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "pod",
			query: prom.BuildQuery("Pod", "dify-new", "dify-new-postgresql-primary-0", prom.CategoryMemory),
			want:  "sum by (pod,namespace) (max by (pod,namespace,container)",
		},
		{
			name:  "workload",
			query: prom.BuildQuery("StatefulSet", "dify-new", "dify-new-postgresql-primary", prom.CategoryMemory),
			want:  "sum by (pod,namespace) (max by (pod,namespace,container)",
		},
		{
			name:  "namespace",
			query: prom.BuildNamespaceQuery("dify-new", prom.CategoryMemory),
			want:  "sum(max by (namespace,pod,container)",
		},
		{
			name:  "cluster",
			query: prom.BuildClusterQuery(prom.CategoryMemory),
			want:  "sum(max by (namespace,pod,container)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(tt.query, tt.want) {
				t.Fatalf("memory query does not dedupe scrape jobs before summing:\nquery: %s\nwant substring: %s", tt.query, tt.want)
			}
		})
	}
}
