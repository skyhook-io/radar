package k8s

import "testing"

func TestFreshOperationContextStillReportsTransitionInProgress(t *testing.T) {
	previous := activeContextOperations.Swap(0)
	defer activeContextOperations.Store(previous)
	before := OperationContext()
	activeContextOperations.Add(1)
	CancelOngoingOperations()
	during := OperationContext()
	if before.Err() == nil || during.Err() != nil {
		t.Fatal("context rotation did not separate old and new operation lifetimes")
	}
	if !ContextOperationInProgress() {
		t.Fatal("fresh operation lifetime hid unfinished cluster transition")
	}
	activeContextOperations.Add(-1)
	if ContextOperationInProgress() || during.Err() != nil {
		t.Fatal("completed transition did not preserve its usable operation lifetime")
	}
}
