package k8s

import (
	"context"
	"log"
	"os"
	"reflect"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// InstalledAt returns when Radar was installed in this cluster, as the
// creation time of its own Deployment in Unix seconds. The Deployment is
// created once at install and patched in place by `helm upgrade`, so the value
// survives pod restarts, rollouts, and replica changes — unlike anything under
// ~/.radar, which lives on an emptyDir that dies with the pod. It changes on
// uninstall and reinstall, which is the intended meaning.
//
// Returns 0 when the Deployment can't be identified or read: no downward-API
// identity from the chart, no cluster access, or RBAC without deployments.
// Callers must treat 0 as "unknown" rather than substituting something else.
//
// A successful read is memoized because the value cannot change while this
// process runs. Failed reads are retried after a short backoff.
func InstalledAt(ctx context.Context) int64 {
	client := GetClient()
	if client == nil {
		return 0
	}
	return installedAtCachedFrom(ctx, client, os.Getenv("MY_POD_NAMESPACE"), os.Getenv("MY_DEPLOYMENT_NAME"))
}

var (
	installedAtMu      sync.Mutex
	installedAtCached  int64
	installedAtTried   time.Time
	installedAtLoading chan struct{}
)

const installedAtRetryAfter = 5 * time.Minute

func installedAtCachedFrom(ctx context.Context, client kubernetes.Interface, namespace, name string) int64 {
	for {
		installedAtMu.Lock()
		if installedAtCached != 0 {
			result := installedAtCached
			installedAtMu.Unlock()
			return result
		}
		if !installedAtTried.IsZero() && time.Since(installedAtTried) < installedAtRetryAfter {
			installedAtMu.Unlock()
			return 0
		}
		if installedAtLoading != nil {
			loading := installedAtLoading
			installedAtMu.Unlock()
			select {
			case <-loading:
				continue
			case <-ctx.Done():
				return 0
			}
		}
		installedAtLoading = make(chan struct{})
		loading := installedAtLoading
		installedAtMu.Unlock()

		result := installedAtFrom(ctx, client, namespace, name)
		installedAtMu.Lock()
		installedAtCached = result
		installedAtTried = time.Now()
		installedAtLoading = nil
		close(loading)
		installedAtMu.Unlock()
		return result
	}
}

func installedAtFrom(ctx context.Context, client kubernetes.Interface, namespace, name string) int64 {
	if namespace == "" || name == "" || isNilClient(client) {
		return 0
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	deploy, err := client.AppsV1().Deployments(namespace).Get(lookupCtx, name, metav1.GetOptions{})
	if err != nil {
		log.Printf("[k8s] install time unavailable (%s/%s unreadable): %v", namespace, name, err)
		return 0
	}
	return deploy.CreationTimestamp.Unix()
}

// isNilClient catches a nil *Clientset that has been placed in an interface:
// the interface itself is then non-nil, so a plain == nil check passes and the
// first method call panics. GetClient returns the concrete type, so every
// caller that forwards it into an interface parameter is exposed to this.
func isNilClient(client kubernetes.Interface) bool {
	if client == nil {
		return true
	}
	v := reflect.ValueOf(client)
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
}
