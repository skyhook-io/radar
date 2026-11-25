package k8score

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/rollouts"
	"github.com/skyhook-io/radar/pkg/workloadrestart"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	ErrRestartNotApplicable   = errors.New("the update strategy would not restart all pods")
	ErrUnsupportedRestartKind = errors.New("only Deployments, StatefulSets, DaemonSets, and Rollouts can be restarted")
)

// RestartReviewedWorkload uses the exact API group for each supported kind,
// even when discovery also serves a CRD with the same name.
func (m *WorkloadManager) RestartReviewedWorkload(ctx context.Context, kind, namespace, name string, review workloadrestart.Preconditions) (workloadrestart.Result, error) {
	if m.dynClient == nil {
		return workloadrestart.Result{}, errors.New("dynamic client not initialized")
	}
	kind = NormalizeWorkloadKind(strings.ToLower(kind))
	if kind == "rollouts" {
		return rollouts.RestartReviewed(ctx, m.dynClient, namespace, name, review)
	}
	if kind != "deployments" && kind != "statefulsets" && kind != "daemonsets" {
		return workloadrestart.Result{}, ErrUnsupportedRestartKind
	}
	builtin, _ := resourceid.BuiltinForName(kind)
	gvr := schema.GroupVersionResource{Group: builtin.Group, Version: builtin.Version, Resource: builtin.Resource}
	validate := func(obj *unstructured.Unstructured) error {
		if !obj.GetDeletionTimestamp().IsZero() {
			return fmt.Errorf("%s %s/%s: %w", obj.GetKind(), namespace, name, ErrWorkloadTerminating)
		}
		behavior := classifyWorkloadUpdateBehavior(kind, obj)
		switch behavior.Type {
		case "paused":
			return fmt.Errorf("Deployment %s/%s: %w", namespace, name, ErrRestartPaused)
		case "onDelete":
			return fmt.Errorf("%w: OnDelete requires manual pod deletion", ErrRestartNotApplicable)
		case "partitioned":
			return fmt.Errorf("%w: the StatefulSet rolling update partition is %d", ErrRestartNotApplicable, *behavior.Partition)
		}
		return nil
	}
	return workloadrestart.Apply(ctx, m.dynClient.Resource(gvr).Namespace(namespace), name, review, validate,
		time.Now(), time.RFC3339Nano, "spec", "template", "metadata", "annotations", "kubectl.kubernetes.io/restartedAt")
}
