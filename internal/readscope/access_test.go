package readscope

import (
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestPartitionAccess(t *testing.T) {
	for _, namespaces := range [][]string{nil, {}, {"unchecked", "denied", "allowed"}} {
		got, denied, unchecked := PartitionAccess(namespaces, func(ns string) (bool, bool) { return ns == "allowed", ns != "unchecked" })
		wantA, wantD, wantU := []string{}, []string{}, []string{}
		if len(namespaces) > 0 {
			wantA, wantD, wantU = []string{"allowed"}, []string{"denied"}, []string{"unchecked"}
		}
		if !reflect.DeepEqual(got, wantA) || !reflect.DeepEqual(denied, wantD) || !reflect.DeepEqual(unchecked, wantU) {
			t.Fatalf("%v: %v / %v / %v", namespaces, got, denied, unchecked)
		}
	}
}

func TestPartitionAccessBoundsConcurrency(t *testing.T) {
	var namespaces []string
	for i := 0; i < 64; i++ {
		namespaces = append(namespaces, fmt.Sprintf("ns-%02d", i))
	}
	var active, peak atomic.Int32
	started := make(chan struct{}, 64)
	release := make(chan struct{})
	done := make(chan []string, 1)
	go func() {
		allowed, _, _ := PartitionAccess(namespaces, func(string) (bool, bool) {
			n := active.Add(1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			return true, true
		})
		done <- allowed
	}()
	for i := 0; i < 16; i++ {
		<-started
	}
	close(release)
	if got := <-done; !reflect.DeepEqual(got, namespaces) {
		t.Fatalf("allowed: %v", got)
	}
	if got := peak.Load(); got != 16 {
		t.Fatalf("peak concurrent checks = %d, want 16", got)
	}
}
