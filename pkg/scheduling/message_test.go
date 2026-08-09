package scheduling

import "testing"

func TestParseMessageDecomposesVerdictAndDropsPreemptionTail(t *testing.T) {
	message := "0/5 nodes are available: 2 Insufficient cpu, 3 node(s) had untolerated taint {dedicated: gpu}. " +
		"preemption: 0/5 nodes are available: 5 No preemption victims found for incoming pod."
	total, reasons := ParseMessage(message)
	if total != 5 {
		t.Fatalf("total nodes = %d, want 5", total)
	}
	if len(reasons) != 2 {
		t.Fatalf("reasons = %+v, want two non-preemption clauses", reasons)
	}
	if reasons[0] != (Reason{Class: InsufficientResource, NodeCount: 2, Resource: "cpu", Raw: "2 Insufficient cpu"}) {
		t.Errorf("CPU reason = %+v", reasons[0])
	}
	if reasons[1] != (Reason{Class: UntoleratedTaint, NodeCount: 3, TaintKey: "dedicated", TaintValue: "gpu", Raw: "3 node(s) had untolerated taint {dedicated: gpu}"}) {
		t.Errorf("taint reason = %+v", reasons[1])
	}
}

func TestParseMessageClasses(t *testing.T) {
	tests := []struct {
		name       string
		clause     string
		class      ReasonClass
		resource   string
		taintKey   string
		taintValue string
	}{
		{name: "CPU", clause: "3 Insufficient cpu", class: InsufficientResource, resource: "cpu"},
		{name: "memory", clause: "3 Insufficient memory", class: InsufficientResource, resource: "memory"},
		{name: "extended resource", clause: "3 Insufficient nvidia.com/gpu", class: InsufficientResource, resource: "nvidia.com/gpu"},
		{name: "pod slots", clause: "3 Too many pods", class: InsufficientResource, resource: "pods"},
		{name: "node affinity", clause: "3 node(s) didn't match Pod's node affinity/selector", class: NodeAffinitySelector},
		{name: "pod affinity", clause: "3 node(s) didn't match pod affinity rules", class: PodAffinity},
		{name: "pod anti-affinity", clause: "3 node(s) didn't match pod anti-affinity rules", class: PodAntiAffinity},
		{name: "topology spread", clause: "3 node(s) didn't match pod topology spread constraints", class: TopologySpread},
		{name: "volume node affinity", clause: "3 node(s) had volume node affinity conflict", class: VolumeNodeAffinity},
		{name: "unbound PVC", clause: "pod has unbound immediate PersistentVolumeClaims", class: VolumeBinding},
		{name: "volume count", clause: "3 node(s) exceed max volume count", class: VolumeCount},
		{name: "host port", clause: "3 node(s) didn't have free ports for the requested pod ports", class: NoPorts},
		{name: "cordoned", clause: "3 node(s) were unschedulable", class: NodeUnschedulable},
		{name: "operator taint", clause: "3 node(s) had untolerated taint {dedicated: gpu}", class: UntoleratedTaint, taintKey: "dedicated", taintValue: "gpu"},
		{name: "lifecycle taint", clause: "3 node(s) had untolerated taint {node.kubernetes.io/not-ready: }", class: NodeUnschedulable, taintKey: "node.kubernetes.io/not-ready"},
		{name: "opaque", clause: "3 node(s) rejected by a future scheduler plugin", class: Other},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, reasons := ParseMessage("0/3 nodes are available: " + test.clause + ".")
			if len(reasons) != 1 {
				t.Fatalf("reasons = %+v, want one", reasons)
			}
			reason := reasons[0]
			if reason.Class != test.class || reason.Resource != test.resource || reason.TaintKey != test.taintKey || reason.TaintValue != test.taintValue {
				t.Errorf("reason = %+v", reason)
			}
			if reason.NodeCount != 3 && test.class != VolumeBinding {
				t.Errorf("node count = %d, want 3", reason.NodeCount)
			}
		})
	}
}

func TestParseMessageWholeEmptyAndIgnoredVariants(t *testing.T) {
	if total, reasons := ParseMessage("pod has unbound immediate PersistentVolumeClaims"); total != 0 || len(reasons) != 1 || reasons[0].Class != VolumeBinding {
		t.Fatalf("whole-message PVC = %d/%+v", total, reasons)
	}
	if total, reasons := ParseMessage(""); total != 0 || reasons != nil {
		t.Fatalf("empty message = %d/%+v", total, reasons)
	}
	if _, reasons := ParseMessage("   "); reasons != nil {
		t.Fatalf("whitespace reasons = %+v", reasons)
	}
	if total, reasons := ParseMessage("0/1 nodes are available: no new claims to deallocate."); total != 1 || len(reasons) != 0 {
		t.Fatalf("ignored DRA-only message = %d/%+v", total, reasons)
	}
}

func TestParseTaint(t *testing.T) {
	tests := []struct {
		clause, key, value string
	}{
		{clause: "3 node(s) had untolerated taint {dedicated: gpu}", key: "dedicated", value: "gpu"},
		{clause: "3 node(s) had untolerated taint {workload}", key: "workload"},
		{clause: "3 node(s) had untolerated taint {node.kubernetes.io/unreachable: }", key: "node.kubernetes.io/unreachable"},
		{clause: "3 node(s) had untolerated taint"},
	}
	for _, test := range tests {
		key, value := ParseTaint(test.clause)
		if key != test.key || value != test.value {
			t.Errorf("ParseTaint(%q) = %q,%q; want %q,%q", test.clause, key, value, test.key, test.value)
		}
	}
}
