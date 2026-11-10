package k8score

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	k8stesting "k8s.io/client-go/testing"
)

func TestDrainNodeDistinguishesFailedVerificationFromPresence(t *testing.T) {
	for _, recoverList := range []bool{false, true} {
		t.Run(fmt.Sprintf("recover=%t", recoverList), func(t *testing.T) {
			shortenDrainPoll(t)
			pod := drainTestPod("web-1")
			pod.UID = "old-web-uid"
			client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}, &pod)
			lists := 0
			client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
				lists++
				if lists == 1 {
					return false, nil, nil
				}
				if recoverList && lists > 2 {
					return true, &corev1.PodList{}, nil
				}
				return true, nil, fmt.Errorf("pod listing unavailable")
			})
			acceptEvictions(client, func() {
				if err := client.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), pod.Namespace, pod.Name); err != nil {
					t.Fatal(err)
				}
			})
			result, err := DrainNode(context.Background(), client, "worker-1", DrainOptions{WaitForDeletion: true, Timeout: 50 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.PendingPods) != 0 {
				t.Fatalf("a failed read is not observed presence: %+v", result)
			}
			if recoverList {
				if result.Outcome != DrainComplete || result.VerificationError != "" || len(result.UnconfirmedPods) != 0 {
					t.Fatalf("recovered read must clear uncertainty: %+v", result)
				}
			} else if result.Outcome != DrainUnconfirmed || result.VerificationError != "pod listing unavailable" || len(result.UnconfirmedPods) != 1 || result.UnconfirmedPods[0] != "shop/web-1" {
				t.Fatalf("missing verification evidence: %+v", result)
			}
		})
	}
}

func TestDrainNodeSkipsFinalVerificationAfterCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pod := drainTestPod("web-1")
	pod.UID = "old-web-uid"
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}, &pod)
	lists := 0
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists++
		return false, nil, nil
	})
	acceptEvictions(client, cancel)
	result, err := DrainNode(ctx, client, "worker-1", DrainOptions{WaitForDeletion: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if lists != 1 {
		t.Fatalf("caller cancellation must prevent further verification reads; got %d lists", lists)
	}
	if result.Outcome != DrainUnconfirmed || result.VerificationError != context.Canceled.Error() || len(result.UnconfirmedPods) != 1 || len(result.PendingPods) != 0 {
		t.Fatalf("canceled verification must stay unconfirmed: %+v", result)
	}
}

func TestDrainTimeout(t *testing.T) {
	tests := []struct {
		name string
		opts DrainOptions
		want time.Duration
	}{
		{name: "explicit timeout wins", opts: DrainOptions{Timeout: 5 * time.Second}, want: 5 * time.Second},
		{name: "explicit timeout wins even when waiting", opts: DrainOptions{Timeout: 5 * time.Second, WaitForDeletion: true}, want: 5 * time.Second},
		{name: "admit-only default", opts: DrainOptions{}, want: defaultDrainTimeout},
		{name: "waiting gets a larger default", opts: DrainOptions{WaitForDeletion: true}, want: defaultDrainWaitTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := drainTimeout(tt.opts); got != tt.want {
				t.Fatalf("drainTimeout(%+v) = %v, want %v", tt.opts, got, tt.want)
			}
		})
	}
	if defaultDrainWaitTimeout <= defaultDrainTimeout {
		t.Fatalf("the wait default (%v) must exceed the admit-only default (%v)", defaultDrainWaitTimeout, defaultDrainTimeout)
	}
}

// shortenDrainPoll speeds up the wait phase so tests do not sleep for whole seconds.
func shortenDrainPoll(t *testing.T) {
	t.Helper()
	restore := drainWaitPollInterval
	drainWaitPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { drainWaitPollInterval = restore })
}

func acceptEvictions(client *fake.Clientset, onAccept func()) {
	client.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		if onAccept != nil {
			onAccept()
		}
		return true, nil, nil
	})
}

func TestDrainNodeWaitsUntilEvictedPodsAreGone(t *testing.T) {
	shortenDrainPoll(t)

	pod := drainTestPod("web-1")
	pod.UID = "uid-web-1"
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}})

	var mu sync.Mutex
	present := true
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		list := &corev1.PodList{}
		if present {
			list.Items = append(list.Items, pod)
		}
		return true, list, nil
	})
	// The eviction is accepted, then the kubelet finishes terminating the pod shortly after.
	acceptEvictions(client, func() {
		go func() {
			time.Sleep(15 * time.Millisecond)
			mu.Lock()
			present = false
			mu.Unlock()
		}()
	})

	res, err := DrainNode(context.Background(), client, "worker-1",
		DrainOptions{IgnoreDaemonSets: true, WaitForDeletion: true, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if !res.WaitedForDeletion {
		t.Fatalf("result must record that the drain waited for deletion")
	}
	if len(res.EvictedPods) != 1 || res.EvictedPods[0] != "shop/web-1" {
		t.Fatalf("want the pod recorded as evicted, got %+v", res.EvictedPods)
	}
	if len(res.PendingPods) != 0 {
		t.Fatalf("no pods should remain once the evicted pod is gone, got %v", res.PendingPods)
	}
}

func TestDrainNodeReportsPendingWhenPodDoesNotLeave(t *testing.T) {
	shortenDrainPoll(t)

	pod := drainTestPod("db-1")
	pod.UID = "uid-db-1"
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}})

	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{Items: []corev1.Pod{pod}}, nil
	})
	acceptEvictions(client, nil) // accepted, but the pod never leaves

	res, err := DrainNode(context.Background(), client, "worker-1",
		DrainOptions{IgnoreDaemonSets: true, WaitForDeletion: true, Timeout: 60 * time.Millisecond})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if !res.WaitedForDeletion {
		t.Fatalf("result must record that the drain waited for deletion")
	}
	if len(res.EvictedPods) != 1 {
		t.Fatalf("the eviction was accepted, want it recorded: %+v", res.EvictedPods)
	}
	if len(res.PendingPods) != 1 || res.PendingPods[0] != "shop/db-1" {
		t.Fatalf("a pod that never leaves must be reported pending, got %v", res.PendingPods)
	}
}

func TestDrainNodeWithoutWaitReturnsOnceEvictionsAccepted(t *testing.T) {
	pod := drainTestPod("web-1")
	pod.UID = "uid-web-1"
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}})

	var mu sync.Mutex
	lists := 0
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		lists++
		mu.Unlock()
		return true, &corev1.PodList{Items: []corev1.Pod{pod}}, nil
	})
	acceptEvictions(client, nil)

	res, err := DrainNode(context.Background(), client, "worker-1",
		DrainOptions{IgnoreDaemonSets: true, WaitForDeletion: false, Timeout: time.Second})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if res.WaitedForDeletion {
		t.Fatalf("the drain must not wait when WaitForDeletion is false")
	}
	if len(res.PendingPods) != 0 {
		t.Fatalf("no pending pods when not waiting, got %v", res.PendingPods)
	}
	mu.Lock()
	n := lists
	mu.Unlock()
	if n != 1 {
		t.Fatalf("without waiting the pods are listed once (the initial drain list), got %d", n)
	}
}

func TestDrainNodeTreatsRecreatedPodAsGone(t *testing.T) {
	shortenDrainPoll(t)

	pod := drainTestPod("web-1")
	pod.UID = "uid-old"
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}})

	var mu sync.Mutex
	recreated := false
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		p := pod
		if recreated {
			p.UID = "uid-new" // same name, a new instance the old eviction did not target
		}
		return true, &corev1.PodList{Items: []corev1.Pod{p}}, nil
	})
	acceptEvictions(client, func() {
		go func() {
			time.Sleep(15 * time.Millisecond)
			mu.Lock()
			recreated = true
			mu.Unlock()
		}()
	})

	res, err := DrainNode(context.Background(), client, "worker-1",
		DrainOptions{IgnoreDaemonSets: true, WaitForDeletion: true, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if len(res.PendingPods) != 0 {
		t.Fatalf("a pod replaced under a new UID is gone, got pending %v", res.PendingPods)
	}
}

// ctxHonouringClient fails pod lists on a done context, as a real API client does; the fake
// clientset ignores the context entirely, which would hide a list made after the deadline.
type ctxHonouringClient struct{ *fake.Clientset }

func (c ctxHonouringClient) CoreV1() corev1client.CoreV1Interface {
	return ctxHonouringCoreV1{c.Clientset.CoreV1()}
}

type ctxHonouringCoreV1 struct{ corev1client.CoreV1Interface }

func (c ctxHonouringCoreV1) Pods(ns string) corev1client.PodInterface {
	return ctxHonouringPods{c.CoreV1Interface.Pods(ns)}
}

type ctxHonouringPods struct{ corev1client.PodInterface }

func (p ctxHonouringPods) List(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.PodInterface.List(ctx, opts)
}

var _ kubernetes.Interface = ctxHonouringClient{}

// A PDB-blocked eviction retries until the shared deadline, so the wait phase starts with the
// deadline already spent. A pod that was evicted and left in the meantime must still be
// reported gone, not pending.
func TestDrainNodeConfirmsDepartedPodsAfterPDBExhaustsDeadline(t *testing.T) {
	shortenDrainPoll(t)

	web := drainTestPod("web-1")
	web.UID = "uid-web-1"
	db := drainTestPod("db-1")
	db.UID = "uid-db-1"
	fakeClient := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}})

	var mu sync.Mutex
	webPresent := true
	fakeClient.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		list := &corev1.PodList{Items: []corev1.Pod{db}}
		if webPresent {
			list.Items = append(list.Items, web)
		}
		return true, list, nil
	})
	fakeClient.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		ev := a.(k8stesting.CreateAction).GetObject().(*policyv1.Eviction)
		if ev.Name == db.Name {
			return true, nil, apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 0)
		}
		mu.Lock()
		webPresent = false
		mu.Unlock()
		return true, nil, nil
	})

	res, err := DrainNode(context.Background(), ctxHonouringClient{fakeClient}, "worker-1",
		DrainOptions{IgnoreDaemonSets: true, WaitForDeletion: true, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if len(res.EvictedPods) != 1 || res.EvictedPods[0] != "shop/web-1" {
		t.Fatalf("want web-1 evicted, got %+v", res.EvictedPods)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("want the PDB-blocked db-1 reported as a failed eviction, got %+v", res.Errors)
	}
	if len(res.PendingPods) != 0 {
		t.Fatalf("web-1 left the node before the final check; it must not be pending, got %v", res.PendingPods)
	}
}
