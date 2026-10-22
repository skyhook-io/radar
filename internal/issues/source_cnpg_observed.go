package issues

import (
	"fmt"
	"sort"
	"time"

	"github.com/skyhook-io/radar/pkg/cnpg"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	ReasonCNPGScheduleDestinationMissing = "CNPGScheduleDestinationMissing"
	ReasonCNPGInstanceReadinessMismatch  = "CNPGInstanceReadinessMismatch"
	ReasonCNPGPrimaryLabelMismatch       = "CNPGPrimaryLabelMismatch"
)

type cnpgPodInventoryProvider interface {
	cnpgInstancePods(*unstructured.Unstructured) ([]*corev1.Pod, bool)
}

func detectCNPGScheduleDestinationIssues(p Provider, gvr schema.GroupVersionResource, schedules []*unstructured.Unstructured) []Issue {
	var clusterGVR schema.GroupVersionResource
	for _, watched := range p.WatchedDynamic() {
		if watched.Group == cnpgGroup && p.KindForGVR(watched) == "Cluster" {
			clusterGVR = watched
			break
		}
	}
	if clusterGVR.Resource == "" {
		return nil
	}
	clustersByNamespace := map[string]map[string]*unstructured.Unstructured{}
	var out []Issue
	for _, schedule := range schedules {
		suspended, _, _ := unstructured.NestedBool(schedule.Object, "spec", "suspend")
		if suspended {
			continue
		}
		ns := schedule.GetNamespace()
		clusters, loaded := clustersByNamespace[ns]
		if !loaded {
			clusters = map[string]*unstructured.Unstructured{}
			list, _ := p.ListDynamic(clusterGVR, ns)
			for _, cluster := range list {
				clusters[cluster.GetName()] = cluster
			}
			clustersByNamespace[ns] = clusters
		}
		cluster := clusters[cnpgSpecClusterName(schedule)]
		if cluster == nil {
			continue
		}
		declaration := cnpg.ParseBackupDeclaration(cluster)
		blocker := declaration.DestinationBlocker(nestedString(schedule.Object, "spec", "method"), nestedString(schedule.Object, "spec", "pluginConfiguration", "name"))
		if blocker == nil {
			continue
		}
		destination := "no backup destination"
		if blocker.Code == "method_destination_missing" {
			destination = "no " + blocker.Method + " destination"
		}
		issue := newConditionIssue(gvr, "ScheduledBackup", ns, schedule.GetName(), SeverityWarning, ReasonCNPGScheduleDestinationMissing,
			fmt.Sprintf("Backup schedule %s cannot run: %s", schedule.GetName(), destination), time.Time{}, false, ReasonCNPGScheduleDestinationMissing, schedule.GetCreationTimestamp().Time)
		issue.RequiredReads = []EvidenceRead{{Group: cnpgGroup, Resource: "clusters", Namespace: ns, Verb: "list"}, {Group: cnpgGroup, Resource: "scheduledbackups", Namespace: ns, Verb: "list"}}
		out = append(out, issue)
	}
	return out
}

func detectCNPGInstanceObservationIssues(p Provider, gvr schema.GroupVersionResource, clusters []*unstructured.Unstructured) []Issue {
	pods, ok := p.(cnpgPodInventoryProvider)
	if !ok {
		return nil
	}
	var out []Issue
	for _, cluster := range clusters {
		if instances, known := pods.cnpgInstancePods(cluster); known {
			out = append(out, cnpgInstanceObservationIssues(gvr, cluster, instances)...)
		}
	}
	return out
}

func cnpgInstanceObservationIssues(gvr schema.GroupVersionResource, cluster *unstructured.Unstructured, pods []*corev1.Pod) []Issue {
	ns, name := cluster.GetNamespace(), cluster.GetName()
	newIssue := func(reason, message string, severity Severity) Issue {
		issue := newConditionIssue(gvr, "Cluster", ns, name, severity, reason, message, time.Time{}, false, reason, cluster.GetCreationTimestamp().Time)
		issue.RequiredReads = []EvidenceRead{{Group: cnpgGroup, Resource: "clusters", Namespace: ns, Verb: "list"}, {Resource: "pods", Namespace: ns, Verb: "list"}}
		return issue
	}
	ready, primaryDown, allReported := 0, false, true
	var labelled []string
	for _, pod := range pods {
		if cnpg.InstanceRole(pod) == "primary" {
			labelled = append(labelled, pod.Name)
		}
		reported := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type != corev1.PodReady {
				continue
			}
			reported = condition.Status == corev1.ConditionTrue || condition.Status == corev1.ConditionFalse
			if condition.Status == corev1.ConditionTrue {
				ready++
			} else if condition.Status == corev1.ConditionFalse && (cnpg.InstanceRole(pod) == "primary" || (cnpg.InstanceRole(pod) != "replica" && pod.Name == nestedString(cluster.Object, "status", "currentPrimary"))) {
				primaryDown = true
			}
		}
		allReported = allReported && reported
	}
	var out []Issue
	statusReady, reported, _ := unstructured.NestedInt64(cluster.Object, "status", "readyInstances")
	if !cnpgHibernated(cluster) && reported && allReported && int64(ready) < statusReady {
		severity := SeverityWarning
		if primaryDown || ready == 0 {
			severity = SeverityCritical
		}
		message := fmt.Sprintf("%d of %d instance Pods not ready%s", len(pods)-ready, len(pods), cnpgPrimaryDownSuffix(primaryDown))
		if int64(len(pods)) < statusReady {
			message = fmt.Sprintf("Only %d ready instance Pods observed; CNPG status reports %d ready", ready, statusReady)
		}
		issue := newIssue(ReasonCNPGInstanceReadinessMismatch, message, severity)
		issue.Cause = fmt.Sprintf("The Pods' Ready condition shows %d ready; CNPG status still reports %d. The status may be stale.", ready, statusReady)
		out = append(out, issue)
	}
	primary := nestedString(cluster.Object, "status", "currentPrimary")
	if primary != "" && len(labelled) > 0 {
		sort.Strings(labelled)
		matches := false
		for _, pod := range labelled {
			matches = matches || pod == primary
		}
		if !matches {
			issue := newIssue(ReasonCNPGPrimaryLabelMismatch,
				fmt.Sprintf("CNPG status names %s primary; the Pod labelled primary is %s", primary, labelled[0]), SeverityWarning)
			issue.Cause = "Status may be stale, or a failover is under way."
			out = append(out, issue)
		}
	}
	return out
}

func cnpgPrimaryDownSuffix(down bool) string {
	if down {
		return ", including the primary"
	}
	return ""
}
