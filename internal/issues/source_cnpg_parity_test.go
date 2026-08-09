package issues

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const cnpgFrontendTaxonomyPath = "../../packages/k8s-ui/src/components/resources/resource-utils-cnpg.ts"

var (
	cnpgTSConstBlock = regexp.MustCompile(`(?s)export const (CNPG_CLUSTER_PHASES_[A-Z]+) = \[(.*?)\] as const`)
	cnpgTSEntry      = regexp.MustCompile(`'([^']*)'`)
	// Prose in the block carries apostrophes and quoted phrases; strip comments
	// before extracting entries or they parse as phases.
	cnpgTSComment = regexp.MustCompile(`(?m)//.*$`)
)

// TestCNPGPhaseTaxonomyMatchesFrontend fails when the Go phase buckets and the
// TypeScript ones drift apart. They are the same taxonomy read by two
// languages: the badge comes from the TS classifier, the issue from the Go
// detector. A divergence ships a red badge with no issue, or an issue with no
// badge — the exact failure mode this integration was repaired for.
//
// All five buckets are compared. The transient bucket matters as much as the
// rest: Go uses it to suppress the instance-shortfall issue during a legitimate
// mid-operation shortfall, so a phase present on one side only would either
// raise a spurious "degraded" issue or silently swallow a real one.
func TestCNPGPhaseTaxonomyMatchesFrontend(t *testing.T) {
	raw, err := os.ReadFile(cnpgFrontendTaxonomyPath)
	if err != nil {
		t.Fatalf("read %s: %v", cnpgFrontendTaxonomyPath, err)
	}

	ts := map[string][]string{}
	for _, m := range cnpgTSConstBlock.FindAllStringSubmatch(string(raw), -1) {
		var phases []string
		for _, e := range cnpgTSEntry.FindAllStringSubmatch(cnpgTSComment.ReplaceAllString(m[2], ""), -1) {
			phases = append(phases, e[1])
		}
		ts[m[1]] = phases
	}
	if len(ts) == 0 {
		t.Fatalf("no phase constants parsed from %s — did the export format change?", cnpgFrontendTaxonomyPath)
	}

	cases := []struct {
		tsConst string
		goSet   map[string]bool
	}{
		{"CNPG_CLUSTER_PHASES_TERMINAL", cnpgTerminalPhases},
		{"CNPG_CLUSTER_PHASES_ATTENTION", cnpgAttentionPhases},
		{"CNPG_CLUSTER_PHASES_TRANSIENT", cnpgTransientPhases},
		{"CNPG_CLUSTER_PHASES_HEALTHY", map[string]bool{cnpgPhaseHealthy: true}},
		{"CNPG_CLUSTER_PHASES_FAILING", map[string]bool{cnpgPhaseFailingOver: true}},
	}

	for _, tc := range cases {
		t.Run(tc.tsConst, func(t *testing.T) {
			tsPhases, ok := ts[tc.tsConst]
			if !ok {
				t.Fatalf("%s not found in %s", tc.tsConst, cnpgFrontendTaxonomyPath)
			}
			var missingInGo, missingInTS []string
			tsSet := map[string]bool{}
			for _, p := range tsPhases {
				tsSet[p] = true
				if !tc.goSet[p] {
					missingInGo = append(missingInGo, p)
				}
			}
			for p := range tc.goSet {
				if !tsSet[p] {
					missingInTS = append(missingInTS, p)
				}
			}
			sort.Strings(missingInGo)
			sort.Strings(missingInTS)
			if len(missingInGo) > 0 {
				t.Errorf("phases in the TS %s but not the Go bucket — badge without issue: %s",
					tc.tsConst, strings.Join(missingInGo, " | "))
			}
			if len(missingInTS) > 0 {
				t.Errorf("phases in the Go bucket but not the TS %s — issue without badge: %s",
					tc.tsConst, strings.Join(missingInTS, " | "))
			}
		})
	}
}
