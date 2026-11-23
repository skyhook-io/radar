package substrate

import "testing"

func TestKagentTemplatePlacement(t *testing.T) {
	tpl := Template{Atespace: "agents", Name: "agent-0123456789ab", CPUMillicores: 1000, MemoryBytes: 1024, WorkerSelector: map[string]string{"pool": "a"}}
	worker := Worker{Namespace: "ate", Pool: "a", State: WorkerActive, Labels: map[string]string{"pool": "a"}, Capacity: &Resources{CPUMillicores: 2000, MemoryBytes: 4096, Actors: 4}, Allocated: &Resources{CPUMillicores: 1500, MemoryBytes: 1024, Actors: 1}}
	got := KagentTemplatePlacement(tpl, []Worker{worker}, true)
	if got.State != "noRoom" {
		t.Fatalf("CPU booked: %+v", got)
	}
	got = KagentTemplatePlacement(tpl, []Worker{worker}, false)
	if got.State != "unknown" {
		t.Fatalf("partial negative: %+v", got)
	}
	worker.Allocated.CPUMillicores = 1000
	got = KagentTemplatePlacement(tpl, []Worker{worker}, false)
	if got.State != "mayFit" || got.Complete {
		t.Fatalf("positive partial: %+v", got)
	}
	worker.Allocated.Actors = 4
	if got := KagentTemplatePlacement(tpl, []Worker{worker}, true); got.State != "full" {
		t.Fatalf("full: %+v", got)
	}
	worker.State = WorkerDraining
	if got := KagentTemplatePlacement(tpl, []Worker{worker}, true); got.State != "noActiveWorkers" {
		t.Fatalf("draining: %+v", got)
	}
}
