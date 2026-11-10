package k8s

import (
	"testing"
	"time"
)

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

func TestCaptureClusterReadsSerializesDependencyReplacement(t *testing.T) {
	capturing := make(chan struct{})
	release := make(chan struct{})
	captured := make(chan bool, 1)
	writerQueued := make(chan struct{})
	written := make(chan struct{})
	generation := 1
	go func() {
		captured <- CaptureClusterReads(func() {
			close(capturing)
			<-release
			if generation != 1 {
				t.Error("dependencies replaced during capture")
			}
		})
	}()
	<-capturing
	go func() {
		activeContextOperations.Add(1)
		close(writerQueued)
		contextOpMu.Lock()
		generation = 2
		contextOpMu.Unlock()
		activeContextOperations.Add(-1)
		close(written)
	}()
	<-writerQueued
	called := false
	if CaptureClusterReads(func() { called = true }) || called {
		t.Error("another capture entered ahead of a queued transition")
	}
	select {
	case <-written:
		t.Error("writer entered during an in-flight capture")
	default:
	}
	close(release)
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("writer did not resume after capture")
	}
	if !<-captured {
		t.Fatal("stable capture was refused")
	}
	if !CaptureClusterReads(func() {
		if generation != 2 {
			t.Error("replacement dependencies not observed")
		}
	}) {
		t.Fatal("capture did not resume after transition")
	}
}

func TestClusterConfigurationWaitsForBriefReadCapture(t *testing.T) {
	contextOpMu.RLock()
	released := make(chan struct{})
	go func() {
		time.Sleep(5 * time.Millisecond)
		contextOpMu.RUnlock()
		close(released)
	}()
	called := false
	err := TryClusterConfiguration(func() error { called = true; return nil })
	<-released
	if err != nil || !called {
		t.Fatalf("configuration refused a brief capture: called=%v err=%v", called, err)
	}
}
