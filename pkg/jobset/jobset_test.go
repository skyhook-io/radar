package jobset

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utiljson "k8s.io/apimachinery/pkg/util/json"
)

type membershipVectors struct {
	JobSet  map[string]any `json:"jobSet"`
	Jobs    []batchv1.Job  `json:"jobs"`
	Members []string       `json:"members"`
}

func loadMembership(t *testing.T) (*unstructured.Unstructured, []batchv1.Job, []string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/membership.json")
	if err != nil {
		t.Fatal(err)
	}
	var v membershipVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: v.JobSet}, v.Jobs, v.Members
}

func TestControlsMatchesMembershipVectors(t *testing.T) {
	root, jobs, want := loadMembership(t)
	var got []string
	for i := range jobs {
		if Controls(root, &jobs[i]) {
			got = append(got, jobs[i].Name)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
}

func TestControlsRejectsUnusableRoots(t *testing.T) {
	root, jobs, _ := loadMembership(t)
	member := &jobs[0]
	if !Controls(root, member) {
		t.Fatal("fixture member should be controlled")
	}
	if Controls(root, nil) {
		t.Fatal("nil child is never controlled")
	}
	noUID := root.DeepCopy()
	noUID.SetUID("")
	if Controls(noUID, member) {
		t.Fatal("a root without a UID cannot prove identity")
	}
	future := root.DeepCopy()
	future.SetAPIVersion(Group + "/v1beta1")
	if Controls(future, member) {
		t.Fatal("an uninterpreted root version controls nothing")
	}
	if Controls(nil, member) {
		t.Fatal("nil root controls nothing")
	}
}

func jobSetObject(spec, status map[string]any) *unstructured.Unstructured {
	obj := map[string]any{"apiVersion": APIVersion, "kind": Kind, "metadata": map[string]any{"name": "train", "namespace": "ml"}}
	if spec != nil {
		obj["spec"] = spec
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

func conditions(entries ...[2]string) []any {
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{"type": e[0], "status": e[1]})
	}
	return out
}

func TestLifecycle(t *testing.T) {
	role := func(fields map[string]any) []any {
		status := map[string]any{"name": "workers"}
		for k, v := range fields {
			status[k] = v
		}
		return []any{status}
	}
	cases := []struct {
		name   string
		spec   map[string]any
		status map[string]any
		want   State
	}{
		{"no status observed is unknown, not pending", nil, nil, State{Phase: PhaseUnknown}},
		{"empty status without role counts is unknown", nil, map[string]any{}, State{Phase: PhaseUnknown}},
		{"reported zero counts are pending", nil, map[string]any{"replicatedJobsStatus": role(map[string]any{"active": int64(0)})}, State{Phase: PhasePending}},
		{"an empty reported list is pending", nil, map[string]any{"replicatedJobsStatus": []any{}}, State{Phase: PhasePending}},
		{"unnamed role entries are ignored", nil, map[string]any{"replicatedJobsStatus": []any{map[string]any{"active": int64(3)}}}, State{Phase: PhasePending}},
		{"child activity is active", nil, map[string]any{"replicatedJobsStatus": role(map[string]any{"active": int64(1)})}, State{Phase: PhaseActive}},
		{"child failures alone are not terminal", nil, map[string]any{"replicatedJobsStatus": role(map[string]any{"failed": int64(1)})}, State{Phase: PhaseActive}},
		{"terminal Failed wins over Completed condition", nil, map[string]any{"terminalState": "Failed", "conditions": conditions([2]string{"Completed", "True"})}, State{Phase: PhaseFinished, Outcome: OutcomeFailed}},
		{"terminal Completed names its condition", nil, map[string]any{"terminalState": "Completed", "conditions": conditions([2]string{"Completed", "True"})}, State{Phase: PhaseFinished, Outcome: OutcomeSucceeded, Condition: "Completed"}},
		{"unknown terminal state stays unknown", nil, map[string]any{"terminalState": "Future", "conditions": conditions([2]string{"Completed", "True"})}, State{Phase: PhaseUnknown}},
		{"Failed condition", nil, map[string]any{"conditions": conditions([2]string{"Failed", "True"})}, State{Phase: PhaseFinished, Outcome: OutcomeFailed, Condition: "Failed"}},
		{"False conditions decide nothing", nil, map[string]any{"conditions": conditions([2]string{"Failed", "False"}), "replicatedJobsStatus": role(nil)}, State{Phase: PhasePending}},
		{"the last entry of a type wins", nil, map[string]any{"conditions": conditions([2]string{"Failed", "True"}, [2]string{"Failed", "False"}), "replicatedJobsStatus": role(nil)}, State{Phase: PhasePending}},
		{"suspended", map[string]any{"suspend": true}, map[string]any{"conditions": conditions([2]string{"Suspended", "True"})}, State{Phase: PhaseSuspended, Condition: "Suspended"}},
		{"resume in order keeps Suspended while starting", map[string]any{"suspend": false}, map[string]any{"conditions": conditions([2]string{"Suspended", "True"}, [2]string{"StartupPolicyInProgress", "True"})}, State{Phase: PhaseActive, Condition: "StartupPolicyInProgress"}},
		{"suspend requested wins over startup", map[string]any{"suspend": true}, map[string]any{"conditions": conditions([2]string{"Suspended", "True"}, [2]string{"StartupPolicyInProgress", "True"})}, State{Phase: PhaseSuspended, Condition: "Suspended"}},
		{"restarting", nil, map[string]any{"conditions": conditions([2]string{"RestartingJobSet", "True"}), "replicatedJobsStatus": []any{}}, State{Phase: PhaseActive, Condition: "RestartingJobSet"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Lifecycle(jobSetObject(tc.spec, tc.status)); got != tc.want {
				t.Fatalf("Lifecycle = %+v, want %+v", got, tc.want)
			}
		})
	}
	other := jobSetObject(nil, map[string]any{"conditions": conditions([2]string{"Failed", "True"})})
	other.SetAPIVersion(Group + "/v1beta1")
	if got := Lifecycle(other); got != (State{Phase: PhaseUnknown}) {
		t.Fatalf("uninterpreted version = %+v, want unknown", got)
	}
}

func TestDeclaredCountsOmittedReplicasAsOne(t *testing.T) {
	obj := jobSetObject(map[string]any{"replicatedJobs": []any{
		map[string]any{"name": "driver"},
		map[string]any{"name": "workers", "replicas": int64(4)},
		"malformed",
	}}, nil)
	roles, jobs := Declared(obj)
	if roles != 3 || jobs != 5 {
		t.Fatalf("Declared = %d roles, %d jobs; want 3, 5", roles, jobs)
	}
}

type lifecycleVector struct {
	Name    string         `json:"name"`
	JobSet  map[string]any `json:"jobSet"`
	Phase   Phase          `json:"phase"`
	Outcome Outcome        `json:"outcome"`
}

func TestLifecycleMatchesSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/lifecycle.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []lifecycleVector `json:"cases"`
	}
	// The apimachinery decoder keeps integers int64, as the API client does.
	if err := utiljson.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, tc := range file.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := Lifecycle(&unstructured.Unstructured{Object: tc.JobSet})
			if got.Phase != tc.Phase || got.Outcome != tc.Outcome {
				t.Fatalf("Lifecycle = %+v, want phase %q outcome %q", got, tc.Phase, tc.Outcome)
			}
		})
	}
}
