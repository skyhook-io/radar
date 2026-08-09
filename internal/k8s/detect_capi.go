package k8s

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/conditions"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func DetectCAPIProblems(dynamicCache *DynamicResourceCache, discovery *ResourceDiscovery, namespace string) []Detection {
	if dynamicCache == nil || discovery == nil {
		return nil
	}

	var problems []Detection
	now := time.Now()

	// Helper: list CAPI resources by kind
	listCAPI := func(kind, group string) []*unstructured.Unstructured {
		if group != "" {
			gvr, ok := discovery.GetGVRWithGroup(kind, group)
			if !ok {
				return nil // CRD not installed — expected
			}
			resources, err := listScoped(dynamicCache, gvr, namespace)
			if err != nil {
				log.Printf("[capi-problems] Failed to list %s (%s): %v", kind, group, err)
				return nil
			}
			return resources
		}
		gvr, ok := discovery.GetGVR(kind)
		if !ok {
			return nil // CRD not installed — expected
		}
		resources, err := listScoped(dynamicCache, gvr, namespace)
		if err != nil {
			log.Printf("[capi-problems] Failed to list %s: %v", kind, err)
			return nil
		}
		return resources
	}

	// Shared condition reader: conditions.FindFalseCondition (one source of truth
	// across the CAPI/GitOps detectors + the issues generic fallback).

	const capiGroup = "cluster.x-k8s.io"
	const capiCPGroup = "controlplane.cluster.x-k8s.io"

	// -----------------------------------------------------------------------
	// CAPI Cluster problems
	// -----------------------------------------------------------------------
	for _, cl := range listCAPI("Cluster", capiGroup) {
		ageDur := now.Sub(cl.GetCreationTimestamp().Time)

		// Phase-based: Failed
		phase, _, _ := unstructured.NestedString(cl.Object, "status", "phase")
		if strings.EqualFold(phase, "failed") {
			problems = append(problems, Detection{
				Kind: "Cluster", Namespace: cl.GetNamespace(), Name: cl.GetName(), Group: capiGroup,
				Severity: "critical", Reason: "Cluster in Failed phase",
				// CAPI records the decisive detail in status.failureMessage/Reason.
				Message: capiFailureDetail(cl),
				Age:     FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				ResourceCreatedAt: cl.GetCreationTimestamp().Time, OnsetUnknown: true,
			})
			continue // don't double-report conditions
		}

		// Condition-based: InfrastructureReady, ControlPlaneReady, Ready, TopologyReconciled
		if condition, ok := conditions.FindFalseConditionWithTime(cl,
			"Ready", "InfrastructureReady", "ControlPlaneReady", "TopologyReconciled",
		); ok {
			ct, reason, msg := condition.Type, condition.Reason, condition.Message
			severity := "high"
			if ct == "InfrastructureReady" || ct == "ControlPlaneReady" {
				severity = "critical"
			}
			displayReason := reason
			if displayReason == "" {
				displayReason = ct + "=False"
			}
			timing, timingBasis := capiIssueTiming(condition.LastTransitionTime, condition.HasLastTransitionTime, cl.GetCreationTimestamp().Time)
			detection := Detection{
				Kind: "Cluster", Namespace: cl.GetNamespace(), Name: cl.GetName(), Group: capiGroup,
				Severity: severity, Reason: displayReason, Message: msg,
				Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				ResourceCreatedAt: cl.GetCreationTimestamp().Time,
				IssueTiming:       timing, IssueTimingBasis: timingBasis,
			}
			setDetectionOnset(&detection, now, condition.LastTransitionTime)
			problems = append(problems, detection)
		}
	}

	// -----------------------------------------------------------------------
	// CAPI Machine problems
	// -----------------------------------------------------------------------
	for _, m := range listCAPI("Machine", "cluster.x-k8s.io") {
		ageDur := now.Sub(m.GetCreationTimestamp().Time)
		phase, _, _ := unstructured.NestedString(m.Object, "status", "phase")

		// Phase-based: Failed
		if strings.EqualFold(phase, "failed") {
			// Prefer CAPI's terminal failureMessage/Reason; fall back to the
			// failing condition message for richer context.
			msg := capiFailureDetail(m)
			if msg == "" {
				_, _, msg, _, _ = conditions.FindFalseCondition(m, "Ready", "InfrastructureReady", "BootstrapReady")
			}
			problems = append(problems, Detection{
				Kind: "Machine", Namespace: m.GetNamespace(), Name: m.GetName(), Group: capiGroup,
				Severity: "critical", Reason: "Machine in Failed phase", Message: msg,
				Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				ResourceCreatedAt: m.GetCreationTimestamp().Time, OnsetUnknown: true,
			})
			continue
		}

		// Phase-based: stuck Provisioning > 10m
		if strings.EqualFold(phase, "provisioning") && ageDur > 10*time.Minute {
			condition, _ := conditions.FindFalseConditionWithTime(m, "InfrastructureReady", "BootstrapReady")
			reason, msg := condition.Reason, condition.Message
			displayReason := fmt.Sprintf("Stuck provisioning for %s", FormatAge(ageDur))
			if reason != "" {
				displayReason += " (" + reason + ")"
			}
			detection := Detection{
				Kind: "Machine", Namespace: m.GetNamespace(), Name: m.GetName(), Group: capiGroup,
				Severity: "high", Reason: displayReason, Message: msg,
				Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				Duration: FormatAge(ageDur), DurationSeconds: int64(ageDur.Seconds()),
				OnsetAt: m.GetCreationTimestamp().Time, ResourceCreatedAt: m.GetCreationTimestamp().Time,
			}
			problems = append(problems, detection)
			continue
		}

		// Condition-based: BootstrapReady=False, NodeHealthy=False, InfrastructureReady=False,
		// with Ready=False as a fallback. Catches problems that phase alone misses,
		// e.g. Running phase but NodeHealthy=False.
		condition, ok := conditions.FindFalseConditionWithTime(m,
			"BootstrapReady", "NodeHealthy", "InfrastructureReady",
		)
		if !ok {
			condition, ok = conditions.FindFalseConditionWithTime(m, "Ready")
		}
		ct, reason, msg := condition.Type, condition.Reason, condition.Message
		if ok && capiConditionCurrent(m, reason) {
			severity := "high"
			if ct == "BootstrapReady" {
				severity = "critical"
			}
			displayReason := reason
			if displayReason == "" {
				displayReason = ct + "=False"
			}
			timing, timingBasis := capiIssueTiming(condition.LastTransitionTime, condition.HasLastTransitionTime, m.GetCreationTimestamp().Time)
			detection := Detection{
				Kind: "Machine", Namespace: m.GetNamespace(), Name: m.GetName(), Group: capiGroup,
				Severity: severity, Reason: displayReason, Message: msg,
				Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				ResourceCreatedAt: m.GetCreationTimestamp().Time,
				IssueTiming:       timing, IssueTimingBasis: timingBasis,
			}
			setDetectionOnset(&detection, now, condition.LastTransitionTime)
			problems = append(problems, detection)
		}
	}

	// -----------------------------------------------------------------------
	// CAPI MachineDeployment problems: ready < desired for > 5m
	// -----------------------------------------------------------------------
	for _, md := range listCAPI("MachineDeployment", capiGroup) {
		desired, _, _ := unstructured.NestedInt64(md.Object, "spec", "replicas")
		ready, _, _ := unstructured.NestedInt64(md.Object, "status", "readyReplicas")
		emitted := false
		if desired > 0 && ready < desired {
			ageDur := now.Sub(md.GetCreationTimestamp().Time)
			if ageDur > 5*time.Minute {
				condition, _ := conditions.FindFalseConditionWithTime(md, "Ready", "Available")
				reason, msg := condition.Reason, condition.Message
				displayReason := fmt.Sprintf("%d/%d machines ready", ready, desired)
				if reason != "" {
					displayReason += " (" + reason + ")"
				}
				detection := Detection{
					Kind: "MachineDeployment", Namespace: md.GetNamespace(), Name: md.GetName(), Group: capiGroup,
					Severity: "high", Reason: displayReason, Message: msg,
					Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
					ResourceCreatedAt: md.GetCreationTimestamp().Time,
				}
				setDetectionOnset(&detection, now, condition.LastTransitionTime)
				problems = append(problems, detection)
				emitted = true
			}
		}
		if emitted {
			continue
		}
		if condition, ok := conditions.FindFalseConditionWithTime(md, "Ready", "Available"); ok && capiConditionCurrent(md, condition.Reason) {
			ageDur := now.Sub(md.GetCreationTimestamp().Time)
			timing, timingBasis := capiIssueTiming(condition.LastTransitionTime, condition.HasLastTransitionTime, md.GetCreationTimestamp().Time)
			detection := Detection{
				Kind: "MachineDeployment", Namespace: md.GetNamespace(), Name: md.GetName(), Group: capiGroup,
				Severity: "high", Reason: capiDisplayReason(condition.Type, condition.Reason), Message: condition.Message,
				Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				ResourceCreatedAt: md.GetCreationTimestamp().Time,
				IssueTiming:       timing, IssueTimingBasis: timingBasis,
			}
			setDetectionOnset(&detection, now, condition.LastTransitionTime)
			problems = append(problems, detection)
		}
	}

	// -----------------------------------------------------------------------
	// CAPI KubeadmControlPlane problems: Ready=False or replicas mismatch
	// -----------------------------------------------------------------------
	for _, kcp := range listCAPI("KubeadmControlPlane", capiCPGroup) {
		ageDur := now.Sub(kcp.GetCreationTimestamp().Time)
		desired, _, _ := unstructured.NestedInt64(kcp.Object, "spec", "replicas")
		ready, _, _ := unstructured.NestedInt64(kcp.Object, "status", "readyReplicas")

		if condition, ok := conditions.FindFalseConditionWithTime(kcp,
			"Ready", "Available", "CertificatesAvailable", "MachinesReady",
		); ok {
			ct, reason, msg := condition.Type, condition.Reason, condition.Message
			severity := "critical"
			displayReason := reason
			if displayReason == "" {
				displayReason = ct + "=False"
			}
			if desired > 0 && ready < desired {
				displayReason = fmt.Sprintf("%d/%d CP replicas ready, %s", ready, desired, displayReason)
			}
			timing, timingBasis := capiIssueTiming(condition.LastTransitionTime, condition.HasLastTransitionTime, kcp.GetCreationTimestamp().Time)
			detection := Detection{
				Kind: "KubeadmControlPlane", Namespace: kcp.GetNamespace(), Name: kcp.GetName(), Group: capiCPGroup,
				Severity: severity, Reason: displayReason, Message: msg,
				Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				ResourceCreatedAt: kcp.GetCreationTimestamp().Time,
				IssueTiming:       timing, IssueTimingBasis: timingBasis,
			}
			setDetectionOnset(&detection, now, condition.LastTransitionTime)
			problems = append(problems, detection)
		}
	}

	// -----------------------------------------------------------------------
	// CAPI MachineHealthCheck: actively remediating
	// -----------------------------------------------------------------------
	for _, mhc := range listCAPI("MachineHealthCheck", capiGroup) {
		expected, _, _ := unstructured.NestedInt64(mhc.Object, "status", "expectedMachines")
		healthy, _, _ := unstructured.NestedInt64(mhc.Object, "status", "currentHealthy")
		emitted := false
		if expected > 0 && healthy < expected {
			ageDur := now.Sub(mhc.GetCreationTimestamp().Time)
			problems = append(problems, Detection{
				Kind: "MachineHealthCheck", Namespace: mhc.GetNamespace(), Name: mhc.GetName(), Group: capiGroup,
				Severity:          "high",
				Reason:            fmt.Sprintf("Remediating: %d/%d healthy", healthy, expected),
				Age:               FormatAge(ageDur),
				AgeSeconds:        int64(ageDur.Seconds()),
				ResourceCreatedAt: mhc.GetCreationTimestamp().Time,
				OnsetUnknown:      true,
			})
			emitted = true
		}
		if emitted {
			continue
		}
		if condition, ok := conditions.FindFalseConditionWithTime(mhc, "Ready", "RemediationAllowed"); ok && capiConditionCurrent(mhc, condition.Reason) {
			ageDur := now.Sub(mhc.GetCreationTimestamp().Time)
			timing, timingBasis := capiIssueTiming(condition.LastTransitionTime, condition.HasLastTransitionTime, mhc.GetCreationTimestamp().Time)
			detection := Detection{
				Kind: "MachineHealthCheck", Namespace: mhc.GetNamespace(), Name: mhc.GetName(), Group: capiGroup,
				Severity: "high", Reason: capiDisplayReason(condition.Type, condition.Reason), Message: condition.Message,
				Age: FormatAge(ageDur), AgeSeconds: int64(ageDur.Seconds()),
				ResourceCreatedAt: mhc.GetCreationTimestamp().Time,
				IssueTiming:       timing, IssueTimingBasis: timingBasis,
			}
			setDetectionOnset(&detection, now, condition.LastTransitionTime)
			problems = append(problems, detection)
		}
	}

	return problems
}

func capiDisplayReason(condType, reason string) string {
	if reason != "" {
		return reason
	}
	return condType + "=False"
}

// capiIssueTiming computes issue_timing for a CAPI condition-based Detection.
// It returns no claim when the condition has no parsed transition timestamp.
func capiIssueTiming(transitionAt time.Time, transitionKnown bool, createdAt time.Time) (timing, basis string) {
	if !transitionKnown {
		return "", ""
	}
	r := IssueTimingFromConditionLTT(transitionAt, createdAt, "condition")
	return r.IssueTiming, r.Basis
}

func capiConditionCurrent(u *unstructured.Unstructured, reason string) bool {
	if conditions.IsInProgressForIssues(reason) {
		return false
	}
	gen := u.GetGeneration()
	if gen == 0 {
		return true
	}
	observed, ok, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	return !ok || observed == 0 || observed >= gen
}

// capiFailureDetail returns a CAPI object's terminal failure detail —
// status.failureMessage (the human string), falling back to status.failureReason
// (the enum). Empty when neither is set. This is the decisive "why" CAPI records
// on a Failed Cluster/Machine, more useful than a generic phase string.
func capiFailureDetail(u *unstructured.Unstructured) string {
	if m, _, _ := unstructured.NestedString(u.Object, "status", "failureMessage"); strings.TrimSpace(m) != "" {
		return m
	}
	r, _, _ := unstructured.NestedString(u.Object, "status", "failureReason")
	return r
}
