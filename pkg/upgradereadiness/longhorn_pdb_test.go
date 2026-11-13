package upgradereadiness

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/skyhook-io/radar/pkg/k8score"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
)

func TestLonghornNativePDBStillBlocksUpgrade(t *testing.T) {
	for _, version := range []string{"1.10.2", "1.11.3", "1.12.2", "1.13.0"} {
		t.Run(version, func(t *testing.T) {
			data, err := os.ReadFile("../../internal/k8s/testdata/longhorn-pdb/v" + version + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var fixture struct {
				PDB policyv1.PodDisruptionBudget `json:"pdb"`
				Pod corev1.Pod                   `json:"pod"`
			}
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Fatal(err)
			}
			input := completeInput()
			input.Namespaces = []string{fixture.PDB.Namespace}
			input.PodDisruptionBudgets = []*policyv1.PodDisruptionBudget{&fixture.PDB}
			input.Pods = []*corev1.Pod{&fixture.Pod}
			result, err := Scan(input, "1.35", "1.36")
			if err != nil {
				t.Fatal(err)
			}
			if got := checkByID(t, result, "node-drain-feasibility"); got.Status != CheckBlocked {
				t.Fatalf("native Longhorn PDB became safe: %+v", got)
			} else if len(got.Findings) != 1 || got.Findings[0].Remediation != k8score.PDBBlockedEvictionAction {
				t.Fatalf("blocked budget must retain controller-aware advice: %+v", got)
			}
			fixture.PDB.Generation++
			result, err = Scan(input, "1.35", "1.36")
			if err != nil {
				t.Fatal(err)
			}
			if got := checkByID(t, result, "node-drain-feasibility"); got.Status != CheckUnknown {
				t.Fatalf("stale native budget must be unknown: %+v", got)
			}
		})
	}
}
