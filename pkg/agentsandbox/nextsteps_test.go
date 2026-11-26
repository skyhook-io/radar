package agentsandbox

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

type imagePullVectors struct {
	RegistryDNSNextStep string `json:"registryDNSNextStep"`
	Cases               []struct {
		Name    string `json:"name"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
		DNS     bool   `json:"dns"`
	} `json:"cases"`
}

// The k8s-ui mirror reads the same vectors.
func TestImagePullNextStepVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/image_pull_next_steps.json")
	if err != nil {
		t.Fatal(err)
	}
	var v imagePullVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.RegistryDNSNextStep != registryDNSNextStep {
		t.Fatalf("vectors pin %q, Go says %q", v.RegistryDNSNextStep, registryDNSNextStep)
	}
	for _, c := range v.Cases {
		want := ""
		if c.DNS {
			want = registryDNSNextStep
		}
		if got := imagePullNextStep(c.Reason, c.Message); got != want {
			t.Errorf("%s: %q, want %q", c.Name, got, want)
		}
	}
}

func TestSchedulerStepFollowsTheMessage(t *testing.T) {
	for _, c := range []struct {
		name, message, want string
	}{
		{"every node at its Pod limit (the live test cluster)", "0/4 nodes are available: 4 Too many pods. no new claims to deallocate, preemption: 0/4 nodes are available: 4 No preemption victims found for incoming pod.", podLimitNextStep},
		{"the bare clause", "2 Too many pods", podLimitNextStep},
		{"the rest are control-plane nodes", "0/3 nodes are available: 1 node(s) had untolerated taint {node-role.kubernetes.io/control-plane: }, 2 Too many pods. preemption: 0/3 nodes are available: 1 Preemption is not helpful for scheduling, 2 No preemption victims found for incoming pod.", podLimitNextStep},
		{"some nodes lack memory too", "0/4 nodes are available: 2 Insufficient memory, 2 Too many pods.", schedulerNextStep},
		{"a taint the Pod could tolerate", "0/3 nodes are available: 1 node(s) had untolerated taint {dedicated: gpu}, 2 Too many pods.", schedulerNextStep},
		{"cordoned nodes", "0/3 nodes are available: 1 node(s) were unschedulable, 2 Too many pods.", schedulerNextStep},
		{"only control-plane nodes", "0/1 nodes are available: 1 node(s) had untolerated taint {node-role.kubernetes.io/control-plane: }.", schedulerNextStep},
		{"not enough CPU", "0/4 nodes are available: 4 Insufficient cpu.", schedulerNextStep},
		{"no message", "", schedulerNextStep},
	} {
		if got := schedulerStep(c.message); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The warm pool on the live test cluster: its sandboxes can't be scheduled
// because every node is at its Pod limit.
func TestPodLimitNextStepOnRows(t *testing.T) {
	if warm := rowFor(Summarize(liveInput()), "SandboxWarmPool", "warm"); warm == nil || warm.NextStep != podLimitNextStep {
		t.Errorf("warm pool row %+v", warm)
	}
	limit := "0/2 nodes are available: 2 Too many pods."
	memory := "0/2 nodes are available: 2 Insufficient memory."
	in := Input{Now: now, Claims: full(), WarmPools: full(), Templates: full(), Controller: running(), Sandboxes: full(
		sandbox("full", time.Hour, conds(cnd("PodScheduled", "False", "Unschedulable", limit, time.Hour), cnd("Ready", "False", "DependenciesNotReady", "Pod exists with phase: Pending", time.Hour))),
		sandbox("big", time.Hour, conds(cnd("PodScheduled", "False", "Unschedulable", memory, time.Hour), cnd("Ready", "False", "DependenciesNotReady", "Pod exists with phase: Pending", time.Hour))),
	)}
	s := Summarize(in)
	if row := rowFor(s, "Sandbox", "full"); row == nil || row.NextStep != podLimitNextStep {
		t.Errorf("full: %+v", row)
	}
	if row := rowFor(s, "Sandbox", "big"); row == nil || row.NextStep != schedulerNextStep {
		t.Errorf("big: %+v", row)
	}
}

// The controller's message on the live cluster names only the waiting reason,
// so the image row keeps its general step; a message showing a failed lookup
// gets the DNS step.
func TestImagePullNextStepOnRows(t *testing.T) {
	if row := rowFor(Summarize(liveInput()), "Sandbox", "bad-image"); row == nil || row.NextStep != failureNextSteps["ImagePullBackOff"] {
		t.Errorf("bad-image: %+v", row)
	}
	dns := `Pod exists with phase: Pending; container "agent" is waiting: ErrImagePull: dial tcp: lookup registry.invalid on 10.0.0.2:53: no such host`
	in := Input{Now: now, Claims: full(), WarmPools: full(), Templates: full(), Controller: running(), Sandboxes: full(
		sandbox("dns", time.Hour, conds(cnd("PodScheduled", "True", "PodScheduled", "", time.Hour), cnd("Ready", "False", "DependenciesNotReady", dns, time.Hour))),
	)}
	if row := rowFor(Summarize(in), "Sandbox", "dns"); row == nil || row.Rule != RuleFailed || row.NextStep != registryDNSNextStep {
		t.Errorf("dns: %+v", row)
	}
}

func TestNextStepsStayShort(t *testing.T) {
	steps := []string{schedulerNextStep, podLimitNextStep, registryDNSNextStep, reconcilerNextStep, unreportedNextStep, defaultFailNextStep}
	for _, s := range failureNextSteps {
		steps = append(steps, s)
	}
	for _, s := range steps {
		if n := len(strings.Fields(s)); n > 25 {
			t.Errorf("%q runs to %d words", s, n)
		}
	}
}
