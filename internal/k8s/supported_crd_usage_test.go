package k8s

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/packages"
)

// Groups Radar supports but deliberately never names in usage reports or the
// package list. Each needs a reason: a group missing from both this list and
// the package map is an integration nobody can measure.
var supportedGroupsNotReported = map[string]string{
	"admissionregistration.k8s.io": "built into Kubernetes",
	"apiextensions.k8s.io":         "built into Kubernetes",
	"apiregistration.k8s.io":       "built into Kubernetes",
	"resource.k8s.io":              "built into Kubernetes",
	"scheduling.k8s.io":            "built into Kubernetes",
	"autoscaling.k8s.io":           "VPA CRDs that some managed clusters install whether or not VPA runs",
	"autoscaling.x-k8s.io":         "ProvisioningRequest, served by Cluster Autoscaler and used by Kueue, so no single owner",
	"k8s.cni.cncf.io":              "NetworkAttachmentDefinition, a standard several CNIs serve besides Multus",
	"snapshot.storage.k8s.io":      "CSI snapshot CRDs with no single owner; managed clusters often preinstall them",
	"helm.crossplane.io":           "a Crossplane provider package; Crossplane's own groups already report it",
	"kubernetes.crossplane.io":     "a Crossplane provider package; Crossplane's own groups already report it",
	"":                             "core PodTemplate, built into Kubernetes",
	"policy.networking.k8s.io":     "AdminNetworkPolicy, a Kubernetes SIG Network API several CNIs serve",
	"eks.amazonaws.com":            "EKS Auto Mode's NodeClass, managed by AWS rather than installed as a package",
	"sinks.knative.dev":            "Knative Eventing's JobSink; eventing.knative.dev already reports Knative Eventing",
}

func TestSupportedCRDGroupsAreReportedOrExcluded(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range supportedCRDFallbacks {
		if seen[r.Group] {
			continue
		}
		seen[r.Group] = true
		_, mapped := packages.IntegrationForCRDGroup(r.Group)
		_, excluded := supportedGroupsNotReported[r.Group]
		switch {
		case mapped && excluded:
			t.Errorf("%s is both mapped in pkg/packages crdGroupToChart and listed as not reported", r.Group)
		case !mapped && !excluded:
			t.Errorf("%s is supported but missing from pkg/packages crdGroupToChart; add it there, or to supportedGroupsNotReported with a reason", r.Group)
		}
	}
	for group := range supportedGroupsNotReported {
		if !seen[group] {
			t.Errorf("supportedGroupsNotReported lists %s, which is no longer in supportedCRDFallbacks", group)
		}
	}
}
