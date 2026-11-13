package issues

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLonghornProtectionRemainsInIssues(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	data, err := os.ReadFile("../k8s/testdata/longhorn-pdb/v1.13.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		PDB policyv1.PodDisruptionBudget `json:"pdb"`
		IM  unstructured.Unstructured    `json:"im"`
		Pod corev1.Pod                   `json:"pod"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	f.PDB.CreationTimestamp = metav1.NewTime(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	onset := f.PDB.CreationTimestamp.Add(time.Hour)
	f.PDB.Status.Conditions = []metav1.Condition{{Type: policyv1.DisruptionAllowedCondition, Status: metav1.ConditionFalse, LastTransitionTime: metav1.NewTime(onset)}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(&f.PDB, &f.Pod)); err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "instancemanagers"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "InstanceManagerList"}, &f.IM)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "InstanceManager", Namespaced: true, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	k8s.WarmupCommonCRDs()
	if !k8s.GetDynamicResourceCache().WaitForSync(gvr, 3*time.Second) {
		t.Fatal("IM not warmed")
	}
	provider := &fakeProvider{problems: k8s.DetectProblems(k8s.GetResourceCache(), f.PDB.Namespace)}
	k8s.ResetTestDynamicState()
	genericProvider := &fakeProvider{problems: k8s.DetectProblems(k8s.GetResourceCache(), f.PDB.Namespace)}
	for _, grouped := range []bool{false, true} {
		genericRows, _ := ComposeWithStats(genericProvider, Filters{Limit: NoLimit, Grouped: grouped})
		if len(genericRows) != 1 || !genericRows[0].FirstSeen.Equal(onset) || genericRows[0].OnsetUnknown {
			t.Fatalf("generic PDB must retain its own condition onset: %+v", genericRows)
		}
		genericTiming := longhornSubjectTiming(genericRows[0])
		var originalID string
		for _, allowEvidence := range []bool{false, true} {
			rows, stats := ComposeWithStats(provider, Filters{Limit: NoLimit, Grouped: grouped, CanReadEvidence: func(EvidenceRead) bool { return allowEvidence }})
			response := NewListResponse(rows, stats)
			if len(rows) != 1 || response.Total != 1 || response.TotalMatched != 1 {
				t.Fatalf("visible warning/count changed: %+v", response)
			}
			row := rows[0]
			if originalID == "" {
				originalID = row.ID
			} else if row.ID != originalID {
				t.Fatal("managed explanation changed issue identity")
			}
			if row.Category != issuesapi.CategoryPDBBlocksEvictions || row.Severity != SeverityWarning {
				t.Fatalf("warning/category changed: %+v", row)
			}
			if got := longhornSubjectTiming(row); !reflect.DeepEqual(got, genericTiming) {
				t.Fatalf("PDB timing changed with Longhorn evidence allowed=%v: got %+v, generic %+v", allowEvidence, got, genericTiming)
			}
			if row.LastSeen.IsZero() || row.LastSeen.Before(genericRows[0].LastSeen) {
				t.Fatalf("recomposed row lost its observation time: %+v", row)
			}
			if strings.Contains(row.Message, "Longhorn is protecting") != allowEvidence {
				t.Fatalf("cross-resource authorization: %+v", row)
			}
			if allowEvidence && !strings.Contains(row.Action, "Let Longhorn manage") {
				t.Fatalf("managed action lost: %+v", row)
			}
			if !allowEvidence && row.Action != k8score.PDBBlockedEvictionAction {
				t.Fatalf("generic action lost: %+v", row)
			}
			if !allowEvidence {
				// Observation time advances on every composition; every other
				// serialized field must match an unrecognized generic PDB.
				normalized := row
				normalized.LastSeen = genericRows[0].LastSeen
				deniedJSON, err := json.Marshal(normalized)
				if err != nil {
					t.Fatal(err)
				}
				genericJSON, err := json.Marshal(genericRows[0])
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(deniedJSON, genericJSON) {
					t.Fatalf("denied evidence changed public row shape: denied=%s generic=%s", deniedJSON, genericJSON)
				}
			}
			wire, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(wire), "Voluntary evictions blocked") {
				t.Fatalf("public response lost finding: %s", wire)
			}
		}
	}
}

type longhornPDBTiming struct {
	FirstSeen         time.Time
	OnsetUnknown      bool
	OnsetCoverage     *issuesapi.OnsetCoverage
	IssueTiming       string
	IssueTimingBasis  string
	TimingSummary     string
	ResourceCreatedAt time.Time
}

func longhornSubjectTiming(issue Issue) longhornPDBTiming {
	return longhornPDBTiming{issue.FirstSeen, issue.OnsetUnknown, issue.OnsetCoverage, issue.IssueTiming, issue.IssueTimingBasis, issue.TimingSummary, issue.ResourceCreatedAt}
}
