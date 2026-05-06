package prometheus

import (
	"strings"
	"testing"
)

func TestMemoryQueriesDedupeScrapeJobsBeforeSumming(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "pod",
			query: BuildQuery("Pod", "dify-new", "dify-new-postgresql-primary-0", CategoryMemory),
			want:  "sum by (pod,namespace) (max by (pod,namespace,container)",
		},
		{
			name:  "workload",
			query: BuildQuery("StatefulSet", "dify-new", "dify-new-postgresql-primary", CategoryMemory),
			want:  "sum by (pod,namespace) (max by (pod,namespace,container)",
		},
		{
			name:  "namespace",
			query: BuildNamespaceQuery("dify-new", CategoryMemory),
			want:  "sum(max by (namespace,pod,container)",
		},
		{
			name:  "cluster",
			query: BuildClusterQuery(CategoryMemory),
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
