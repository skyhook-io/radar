package k8score

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/tools/cache"
)

// fakePagedPods serves `total` pods through the limit/continue protocol so we
// can exercise newPagedInformer's pager without a real apiserver (the fake
// clientset ignores Limit/Continue). The continue token is just the next
// offset encoded as a string.
func fakePagedPods(total int, calls *int32) func(ctx context.Context, opts metav1.ListOptions) (apiruntime.Object, error) {
	return func(_ context.Context, opts metav1.ListOptions) (apiruntime.Object, error) {
		atomic.AddInt32(calls, 1)
		start := 0
		if opts.Continue != "" {
			// token is "<offset>"
			for _, c := range opts.Continue {
				start = start*10 + int(c-'0')
			}
		}
		end := total
		if opts.Limit > 0 && start+int(opts.Limit) < end {
			end = start + int(opts.Limit)
		}
		list := &corev1.PodList{ListMeta: metav1.ListMeta{ResourceVersion: "1"}}
		for i := start; i < end; i++ {
			list.Items = append(list.Items, corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-" + itoa(i), Namespace: "default", ResourceVersion: "1"},
			})
		}
		if end < total { // more remain → hand back a continue token
			list.Continue = itoa(end)
		}
		return list, nil
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestNewPagedInformer_AssemblesAllPages(t *testing.T) {
	// Pagination is the LIST-fallback path: it only runs when WatchList
	// streaming is unavailable. With WatchListClient on (the client-go
	// default), the reflector would stream and bypass our ListFunc entirely.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)

	const pageSize = 5
	cases := []struct {
		name          string
		total         int
		wantMultiPage bool
	}{
		{"empty", 0, false},
		{"under one page", 3, false},
		{"exactly one page", pageSize, false},
		{"one over a page", pageSize + 1, true},
		{"several pages", pageSize*2 + 2, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			fw := watch.NewFake()
			inf := newPagedInformer(&corev1.Pod{}, pageSize,
				fakePagedPods(tc.total, &calls),
				func(_ context.Context, _ metav1.ListOptions) (watch.Interface, error) { return fw, nil },
			)

			stop := make(chan struct{})
			defer close(stop)
			go inf.Run(stop)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if !cache.WaitForCacheSync(ctx.Done(), inf.HasSynced) {
				t.Fatal("informer did not sync")
			}

			if got := len(inf.GetStore().ListKeys()); got != tc.total {
				t.Errorf("store has %d pods, want %d (pagination dropped or duplicated items)", got, tc.total)
			}
			gotCalls := atomic.LoadInt32(&calls)
			if tc.wantMultiPage && gotCalls < 2 {
				t.Errorf("expected pagination (>=2 list calls) for %d items at pageSize %d, got %d", tc.total, pageSize, gotCalls)
			}
			if gotCalls < 1 {
				t.Errorf("expected at least one list call, got %d", gotCalls)
			}
		})
	}
}
