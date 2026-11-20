package k8score

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

// KueueQueueIndex files Kueue Workloads by the queues they name and
// LocalQueues by the ClusterQueue they point at, so a queue's Workloads are
// found without reading every Workload. It records what each object says
// now; whether that makes a Workload a member of a queue is the reader's
// decision.
const KueueQueueIndex = "kueueQueue"

// KueueSubmissionKey files a Workload in namespace under the LocalQueue its
// spec.queueName names.
func KueueSubmissionKey(namespace, localQueue string) string {
	return strings.Join([]string{"s", namespace, localQueue}, "\x00")
}

// KueueReservationKey files a Workload under the ClusterQueue its
// status.admission.clusterQueue names.
func KueueReservationKey(clusterQueue string) string {
	return "r\x00" + clusterQueue
}

// KueueLocalQueueTargetKey files a LocalQueue under the ClusterQueue its
// spec.clusterQueue names.
func KueueLocalQueueTargetKey(clusterQueue string) string {
	return "c\x00" + clusterQueue
}

var kueueQueueIndexers = map[schema.GroupResource]cache.Indexers{
	{Group: "kueue.x-k8s.io", Resource: "workloads"}:   {KueueQueueIndex: kueueQueueKeys(workloadQueueKeys)},
	{Group: "kueue.x-k8s.io", Resource: "localqueues"}: {KueueQueueIndex: kueueQueueKeys(localQueueTargetKeys)},
}

// The field paths are the same in v1beta1 and v1beta2.
func workloadQueueKeys(u *unstructured.Unstructured) []string {
	var keys []string
	if queue, _, _ := unstructured.NestedString(u.Object, "spec", "queueName"); queue != "" {
		keys = append(keys, KueueSubmissionKey(u.GetNamespace(), queue))
	}
	if clusterQueue, _, _ := unstructured.NestedString(u.Object, "status", "admission", "clusterQueue"); clusterQueue != "" {
		keys = append(keys, KueueReservationKey(clusterQueue))
	}
	return keys
}

func localQueueTargetKeys(u *unstructured.Unstructured) []string {
	if clusterQueue, _, _ := unstructured.NestedString(u.Object, "spec", "clusterQueue"); clusterQueue != "" {
		return []string{KueueLocalQueueTargetKey(clusterQueue)}
	}
	return nil
}

func kueueQueueKeys(keys func(*unstructured.Unstructured) []string) cache.IndexFunc {
	return func(obj any) ([]string, error) {
		if q, isQuery := obj.(indexQuery); isQuery {
			return q, nil
		}
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, nil
		}
		return keys(u), nil
	}
}
