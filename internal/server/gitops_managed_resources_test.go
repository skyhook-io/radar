package server

import "testing"

// TestManagedScanDenyList pins the deny-list contents that
// handleGitOpsManagedResources uses to filter the dynamic discovery
// output. Verifying the list shape here (rather than via an
// integration test that mocks discovery) keeps the contract obvious:
// adding or removing a Kind is a single-file change with an explicit
// regression check.
//
// The list answers "which Kinds does Argo NEVER stamp with the tracking
// annotation, so they'd never match anyway and just slow down the scan?"
// Two categories:
//  1. Descendants — owned by Argo-managed resources, walked from owners
//     by the tree builder (Pod, ReplicaSet, ControllerRevision,
//     Endpoints, EndpointSlice).
//  2. Platform-internal noise — never user-managed (Event, Lease,
//     FlowSchema, PriorityLevelConfiguration, TokenRequest, the *Review
//     subject-action stubs, Binding).
//
// NOT a security boundary — filterGitOpsTreeForUser is what gates what
// each caller sees per-resource.
func TestManagedScanDenyList(t *testing.T) {
	// Owned-by-managed-resource kinds we never want to scan directly.
	descendants := []string{
		"Pod", "ReplicaSet", "ControllerRevision",
		"Endpoints", "EndpointSlice",
	}
	for _, k := range descendants {
		if !managedScanDenyList[k] {
			t.Errorf("descendant kind %q must be in managedScanDenyList", k)
		}
	}

	// Platform-internal noise.
	noise := []string{
		"Event", "Lease",
		"FlowSchema", "PriorityLevelConfiguration",
		"TokenRequest", "TokenReview",
		"SubjectAccessReview", "SelfSubjectAccessReview",
		"LocalSubjectAccessReview", "SelfSubjectRulesReview",
		"Binding",
	}
	for _, k := range noise {
		if !managedScanDenyList[k] {
			t.Errorf("platform-noise kind %q must be in managedScanDenyList", k)
		}
	}

	// Things that ARE user-managed and must NOT be in the deny-list.
	// Regression-prone: a future "let's expand the deny-list" PR could
	// accidentally hide common workload kinds. This pins the contract.
	mustNotDeny := []string{
		"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob",
		"Service", "ConfigMap", "Secret",
		"Ingress", "HTTPRoute",
		"Certificate", "ClusterIssuer",
		"Rollout", "Application", "AppProject",
		"ClusterRole", "ClusterRoleBinding", "Namespace",
	}
	for _, k := range mustNotDeny {
		if managedScanDenyList[k] {
			t.Errorf("user-managed kind %q must NOT be in managedScanDenyList (would hide it from the fleet detail tree)", k)
		}
	}
}
