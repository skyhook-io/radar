package issues

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const cnpgGoldenMatrixPath = "../../testdata/cnpg/badge-issue-matrix.json"

type cnpgGoldenCase struct {
	Name     string         `json:"name"`
	Spec     map[string]any `json:"spec"`
	Status   map[string]any `json:"status"`
	Metadata map[string]any `json:"metadata"`
	Badge    struct {
		Text  string `json:"text"`
		Level string `json:"level"`
	} `json:"badge"`
	Issues []string `json:"issues"`
	Worst  string   `json:"worst"`
}

type cnpgGoldenFile struct {
	Cases []cnpgGoldenCase `json:"cases"`
}

func loadCNPGGolden(t *testing.T) cnpgGoldenFile {
	t.Helper()
	raw, err := os.ReadFile(cnpgGoldenMatrixPath)
	if err != nil {
		t.Fatalf("read %s: %v", cnpgGoldenMatrixPath, err)
	}
	var f cnpgGoldenFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse %s: %v", cnpgGoldenMatrixPath, err)
	}
	if len(f.Cases) == 0 {
		t.Fatalf("no cases in %s", cnpgGoldenMatrixPath)
	}
	return f
}

// TestCNPGGoldenMatrix asserts the Go detector against the shared specification
// in testdata/cnpg/badge-issue-matrix.json. The TypeScript badge is asserted
// against the SAME file in resource-utils-cnpg.golden.test.ts, so a change to
// one language that the other does not mirror fails on both sides.
//
// The phase-spelling parity test cannot catch precedence, readiness-presence or
// condition differences — every badge/issue disagreement found on this branch
// slipped past it. This is the behavioural guard.
func TestCNPGGoldenMatrix(t *testing.T) {
	for _, tc := range loadCNPGGolden(t).Cases {
		t.Run(tc.Name, func(t *testing.T) {
			// JSON numbers decode as float64; the detector reads int64.
			spec := normalizeJSONInts(tc.Spec)
			status := normalizeJSONInts(tc.Status)
			// Fixed identity plus any annotations the case carries (hibernation,
			// fencing) — those live on metadata, not status, and the detector reads
			// them through GetAnnotations.
			metadata := map[string]any{"name": "pg", "namespace": "pg"}
			if annos, ok := tc.Metadata["annotations"].(map[string]any); ok {
				metadata["annotations"] = annos
			}
			u := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "postgresql.cnpg.io/v1",
				"kind":       "Cluster",
				"metadata":   metadata,
				"spec":       spec,
				"status":     status,
			}}

			issues := detectCNPGIssues(cnpgClusterGVR, "Cluster", u)
			var got []string
			worst := "none"
			for _, i := range issues {
				got = append(got, i.Reason)
				switch {
				case i.Severity == SeverityCritical:
					worst = "critical"
				case worst == "none":
					worst = string(i.Severity)
				}
			}
			want := append([]string(nil), tc.Issues...)
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("issues = [%s], want [%s]", strings.Join(got, ","), strings.Join(want, ","))
			}
			if worst != tc.Worst {
				t.Errorf("worst severity = %q, want %q", worst, tc.Worst)
			}
		})
	}
}

// normalizeJSONInts converts float64 values decoded from JSON into int64 so
// unstructured.NestedInt64 can read them, mirroring what the API server stores.
func normalizeJSONInts(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch t := v.(type) {
		case float64:
			out[k] = int64(t)
		case map[string]any:
			out[k] = normalizeJSONInts(t)
		case []any:
			list := make([]any, 0, len(t))
			for _, item := range t {
				if im, ok := item.(map[string]any); ok {
					list = append(list, normalizeJSONInts(im))
					continue
				}
				list = append(list, item)
			}
			out[k] = list
		default:
			out[k] = v
		}
	}
	return out
}

// Buckets must be disjoint. The two languages check them in different orders
// (TS: transient before attention; Go: attention before transient), so a phase
// in both maps would classify differently per language while the spelling
// parity test still passed.
func TestCNPGPhaseBucketsAreDisjoint(t *testing.T) {
	buckets := map[string]map[string]bool{
		"terminal":  cnpgTerminalPhases,
		"transient": cnpgTransientPhases,
		"attention": cnpgAttentionPhases,
	}
	seen := map[string]string{}
	for name, set := range buckets {
		for phase := range set {
			if prev, dup := seen[phase]; dup {
				t.Errorf("phase %q is in both %s and %s buckets", phase, prev, name)
			}
			seen[phase] = name
		}
	}
	for _, single := range []struct{ name, phase string }{
		{"healthy", cnpgPhaseHealthy},
		{"failing", cnpgPhaseFailingOver},
	} {
		if prev, dup := seen[single.phase]; dup {
			t.Errorf("phase %q is in both %s and %s buckets", single.phase, prev, single.name)
		}
		seen[single.phase] = single.name
	}
}
