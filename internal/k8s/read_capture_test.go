package k8s

import "testing"

func TestCaptureClusterReadsRefusesTransitionWithoutBlocking(t *testing.T) {
	called := false
	contextOpMu.Lock()
	if CaptureClusterReads(func() { called = true }) {
		contextOpMu.Unlock()
		t.Fatal("captured dependencies during a transition")
	}
	contextOpMu.Unlock()
	if called {
		t.Fatal("callback ran during a transition")
	}
	activeContextOperations.Add(1)
	captured := CaptureClusterReads(func() { called = true })
	activeContextOperations.Add(-1)
	if captured || called {
		t.Fatal("captured dependencies while a context change was queued")
	}
	if !CaptureClusterReads(func() { called = true }) || !called {
		t.Fatal("stable dependencies were not captured")
	}
}

func TestCaptureClusterReadsAllowsConcurrentCaptures(t *testing.T) {
	contextOpMu.RLock()
	defer contextOpMu.RUnlock()
	called := false
	if !CaptureClusterReads(func() { called = true }) || !called {
		t.Fatal("an independent read capture blocked stable dependencies")
	}
}
