package server

import (
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestMergeNamespaceCapabilitiesUsesCleanNamespaceDenies(t *testing.T) {
	caps := &k8s.Capabilities{
		Exec:        true,
		Logs:        true,
		PortForward: true,
		WorkloadWrites: k8s.WorkloadWritePermissions{
			Deployments:  true,
			DaemonSets:   true,
			StatefulSets: true,
			Rollouts:     true,
		},
	}
	nsCaps := &k8s.NamespaceCapabilities{}

	mergeNamespaceCapabilities(caps, nsCaps)

	if caps.Exec || caps.Logs || caps.PortForward {
		t.Fatalf("clean namespace denies should override global exec/log/port-forward grants: %+v", caps)
	}
	if caps.WorkloadWrites.Deployments || caps.WorkloadWrites.DaemonSets || caps.WorkloadWrites.StatefulSets || caps.WorkloadWrites.Rollouts {
		t.Fatalf("clean namespace denies should override global workload write grants: %+v", caps.WorkloadWrites)
	}
}

func TestMergeNamespaceCapabilitiesPreservesGlobalOnErroredChecks(t *testing.T) {
	caps := &k8s.Capabilities{
		Exec:        true,
		Logs:        true,
		PortForward: true,
		WorkloadWrites: k8s.WorkloadWritePermissions{
			Deployments:  true,
			DaemonSets:   true,
			StatefulSets: true,
			Rollouts:     true,
		},
	}
	nsCaps := &k8s.NamespaceCapabilities{
		Errors: k8s.NamespaceCapabilityErrors{
			Exec: true,
			WorkloadWrites: k8s.WorkloadWriteCapabilityErrors{
				Deployments: true,
			},
		},
	}

	mergeNamespaceCapabilities(caps, nsCaps)

	if !caps.Exec {
		t.Fatal("errored exec check should preserve existing global grant")
	}
	if caps.Logs || caps.PortForward {
		t.Fatalf("clean namespace denies should still override non-errored grants: logs=%v portForward=%v", caps.Logs, caps.PortForward)
	}
	if !caps.WorkloadWrites.Deployments {
		t.Fatal("errored deployment write check should preserve existing global grant")
	}
	if caps.WorkloadWrites.DaemonSets || caps.WorkloadWrites.StatefulSets || caps.WorkloadWrites.Rollouts {
		t.Fatalf("clean workload write denies should still override non-errored grants: %+v", caps.WorkloadWrites)
	}
}

// Helm writes are namespace-scoped like the rest: a user who can create Secrets
// only in their team's namespace gets the Helm buttons there and nowhere else.
func TestMergeNamespaceCapabilitiesHelmWrite(t *testing.T) {
	caps := &k8s.Capabilities{HelmWrite: false}
	mergeNamespaceCapabilities(caps, &k8s.NamespaceCapabilities{HelmWrite: true})
	if !caps.HelmWrite {
		t.Fatal("a clean namespace grant should enable Helm writes there")
	}

	caps = &k8s.Capabilities{HelmWrite: true}
	mergeNamespaceCapabilities(caps, &k8s.NamespaceCapabilities{})
	if caps.HelmWrite {
		t.Fatal("a clean namespace deny should override a global Helm write grant")
	}
}
