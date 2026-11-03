package kueuevisibility

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/schedulinginsight"
)

// agentBudget bounds the read an agent's resource context waits for.
const agentBudget = 3 * time.Second

// Positions is the process-wide reader. Its answers are reused only within
// one cluster connection and one identity.
var Positions = NewReader()

// RequestFor builds a read as the context's user (Radar's own identity when
// none is signed in) on the current cluster connection, and returns that
// connection's binding so the caller can drop the answer if the connection
// changes before it is used.
func RequestFor(ctx context.Context, workload *unstructured.Unstructured) (Request, string) {
	user := pkgauth.UserFromContext(ctx)
	req := Request{Workload: workload, Identity: IdentityOf(user)}
	var binding string
	if user != nil {
		client, b, err := k8s.UserClientSafetySnapshot(user)
		binding = b
		if err == nil && client != nil {
			req.Client = client
		}
	} else {
		client, b := k8s.GetClientSafetySnapshot()
		binding = b
		if client != nil {
			req.Client = kubernetes.Interface(client)
		}
	}
	if discovery := k8s.GetResourceDiscovery(); discovery != nil && discovery.ResourceDiscovery != nil {
		req.Discovery = discovery.ResourceDiscovery
	}
	if !pkgauth.UserTokenMode() {
		req.MemoKey = binding + "\x00"
		if user != nil {
			req.MemoKey += pkgauth.IdentityCacheKey(user.Username, user.Groups)
		}
	}
	return req, binding
}

// CurrentBinding is the binding of Radar's current cluster connection.
func CurrentBinding() string {
	_, binding := k8s.GetClientSafetySnapshot()
	return binding
}

// Attach adds Kueue's reported position to the Kueue observation of a
// Workload eligible for a pending-position lookup, read as the context's
// user. It leaves the summary unchanged for any other object, and drops the
// answer if the cluster connection changed while reading.
func Attach(ctx context.Context, summary *resourcecontext.SchedulingSummary, obj runtime.Object) {
	if summary == nil || !schedulinginsight.KueueWorkloadEligibleForPendingPosition(obj) {
		return
	}
	workload, _ := obj.(*unstructured.Unstructured)
	for i := range summary.Observations {
		observation := &summary.Observations[i]
		if observation.Source != resourcecontext.SchedulingSourceKueue || observation.Kueue == nil {
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, agentBudget)
		defer cancel()
		req, binding := RequestFor(ctx, workload)
		position := Positions.Read(readCtx, req)
		if binding != CurrentBinding() {
			return
		}
		observation.Kueue.PendingPosition = position
		return
	}
}
