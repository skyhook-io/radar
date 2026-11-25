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

func TestPodDebugNeedsBothNamespacePermissions(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "false")
	for _, exec := range []bool{false, true} {
		for _, update := range []bool{false, true} {
			caps := &k8s.Capabilities{Exec: true, PodDebug: true}
			mergeNamespaceCapabilities(caps, &k8s.NamespaceCapabilities{Exec: exec, PodDebug: update})
			if caps.PodDebug != (exec && update) {
				t.Errorf("exec=%v, update ephemeralcontainers=%v: podDebug=%v", exec, update, caps.PodDebug)
			}
		}
	}
}

func TestPodDebugHiddenInCloudMode(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "true")
	caps := &k8s.Capabilities{}
	mergeNamespaceCapabilities(caps, &k8s.NamespaceCapabilities{Exec: true, PodDebug: true})
	if caps.PodDebug {
		t.Fatal("hosted view offered the direct debug action")
	}
}

func TestMissingShellPermissionHint(t *testing.T) {
	for _, tc := range []struct {
		name, cloud  string
		ns           k8s.NamespaceCapabilities
		open, denied bool
	}{
		{"exec only", "false", k8s.NamespaceCapabilities{Exec: true}, true, true},
		{"update granted", "false", k8s.NamespaceCapabilities{Exec: true, PodDebug: true}, true, false},
		{"update unresolved", "false", k8s.NamespaceCapabilities{Exec: true, Errors: k8s.NamespaceCapabilityErrors{PodDebug: true}}, true, false},
		{"exec unresolved", "false", k8s.NamespaceCapabilities{Errors: k8s.NamespaceCapabilityErrors{Exec: true}}, false, false},
		{"exec disabled or denied", "false", k8s.NamespaceCapabilities{}, false, false},
		{"hosted", "true", k8s.NamespaceCapabilities{Exec: true}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RADAR_CLOUD_MODE", tc.cloud)
			caps := &k8s.Capabilities{Exec: true}
			mergeNamespaceCapabilities(caps, &tc.ns)
			if caps.PodExecUID != tc.open || caps.PodDebugUpdateDenied != tc.denied {
				t.Fatalf("open=%v denied=%v", caps.PodExecUID, caps.PodDebugUpdateDenied)
			}
		})
	}
}
