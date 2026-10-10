package insights

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestMatchesFinalizerController(t *testing.T) {
	root := &unstructured.Unstructured{}
	for _, tt := range []struct {
		name, group, finalizer, workload string
		labels                           map[string]string
		want                             bool
	}{
		{name: "AWS node class", group: "karpenter.k8s.aws", finalizer: "karpenter.k8s.aws/termination", workload: "karpenter", want: true},
		{name: "Azure node class", group: "karpenter.azure.com", finalizer: "karpenter.azure.com/termination", workload: "karpenter", want: true},
		{name: "karpenter deployment", group: "karpenter.sh", finalizer: "karpenter.sh/termination", workload: "karpenter", want: true},
		{name: "catalog label", group: "operator.victoriametrics.com", finalizer: "apps.victoriametrics.com/finalizer", workload: "custom", labels: map[string]string{"app.kubernetes.io/name": "victoria-metrics-operator"}, want: true},
		{name: "catalog wrong group", group: "unrelated.test", finalizer: "apps.victoriametrics.com/finalizer", workload: "victoria-metrics-operator"},
		{name: "name", group: "widgets.example.com", finalizer: "widgets.example.com/cleanup", workload: "release-widgets-operator", want: true},
		{name: "part-of", group: "widgets.example.com", finalizer: "widgets.example.com/cleanup", workload: "custom", labels: map[string]string{"app.kubernetes.io/part-of": "widgets-operator"}, want: true},
		{name: "unrelated domain", group: "other.example.com", finalizer: "widgets.example.com/cleanup", workload: "widgets-operator"},
		{name: "domain only", group: "widgets.example.com", finalizer: "widgets.example.com/cleanup", workload: "widgets-server"},
		{name: "substring", group: "widgets.example.com", finalizer: "widgets.example.com/cleanup", workload: "notwidgets-operator"},
		{name: "reserved domain", group: "k8s.io", finalizer: "k8s.io/cleanup", workload: "k8s-operator"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root.SetAPIVersion(tt.group + "/v1")
			workload := &metav1.ObjectMeta{Name: tt.workload, Labels: tt.labels}
			if got := MatchesFinalizerController(tt.finalizer, root, workload); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestResolveKarpenterProviderFinalizerOwner(t *testing.T) {
	for _, key := range []string{"karpenter.sh/termination", "karpenter.sh/custom", "karpenter.k8s.aws/termination", "karpenter.azure.com/termination"} {
		t.Run(key, func(t *testing.T) {
			owner := ResolveFinalizerOwner(key, &unstructured.Unstructured{})
			if owner == nil || owner.Controller != "karpenter" || owner.Namespace != "" || owner.SelectorKey != "app.kubernetes.io/name" || owner.SelectorValue != "karpenter" || owner.OffClusterOfferings == "" || !owner.ReleasesInfrastructure {
				t.Fatalf("got owner %+v", owner)
			}
		})
	}
}

func TestFinalizerOwnerOffClusterAndInfrastructure(t *testing.T) {
	argo := ResolveFinalizerOwner("resources-finalizer.argocd.argoproj.io", &unstructured.Unstructured{})
	if argo == nil || argo.OffClusterOfferings != "Amazon EKS Capabilities" || argo.ReleasesInfrastructure {
		t.Fatalf("argo=%+v", argo)
	}
	vm := &unstructured.Unstructured{}
	vm.SetAPIVersion("operator.victoriametrics.com/v1beta1")
	for _, owner := range []*FinalizerOwner{ResolveFinalizerOwner("apps.victoriametrics.com/finalizer", vm), ResolveFinalizerOwner("finalizers.helm.toolkit.fluxcd.io", vm)} {
		if owner == nil || owner.OffClusterOfferings != "" || owner.ReleasesInfrastructure {
			t.Fatalf("in-cluster catalog entry=%+v", owner)
		}
	}
}

// Infrastructure operators stay out of the catalog: absent an identified
// controller, callers must not treat a missing pod as permission to remove
// the finalizer.
func TestInfrastructureOperatorFinalizersAreNotIdentified(t *testing.T) {
	for finalizer, group := range map[string]string{
		"finalizer.managedresource.crossplane.io": "ec2.aws.upbound.io",
		"composite.apiextensions.crossplane.io":   "platform.example.org",
		"cluster.cluster.x-k8s.io":                "cluster.x-k8s.io",
		"cnrm.cloud.google.com/finalizer":         "sql.cnrm.cloud.google.com",
		"finalizers.s3.services.k8s.aws":          "s3.services.k8s.aws",
		"strimzi.io/topic-operator":               "kafka.strimzi.io",
	} {
		root := &unstructured.Unstructured{}
		root.SetAPIVersion(group + "/v1")
		if owner := ResolveFinalizerOwner(finalizer, root); owner != nil {
			t.Errorf("%s resolved to %+v", finalizer, owner)
		}
		for _, workload := range []string{"crossplane", "capi-controller-manager", "cnrm-controller-manager", "ack-s3-controller", "strimzi-cluster-operator"} {
			if MatchesFinalizerController(finalizer, root, &metav1.ObjectMeta{Name: workload}) {
				t.Errorf("%s matched workload %s", finalizer, workload)
			}
		}
	}
}

func TestFluxImageFinalizerOwnerByKind(t *testing.T) {
	for kind, want := range map[string]string{"ImageUpdateAutomation": "image-automation-controller", "ImagePolicy": "image-reflector-controller", "ImageRepository": "image-reflector-controller"} {
		root := &unstructured.Unstructured{}
		root.SetAPIVersion("image.toolkit.fluxcd.io/v1beta2")
		root.SetKind(kind)
		for _, finalizer := range []string{"finalizers.fluxcd.io", "finalizers.image.toolkit.fluxcd.io"} {
			if owner := ResolveFinalizerOwner(finalizer, root); owner == nil || owner.Controller != want || owner.SelectorValue != want {
				t.Errorf("%s %s: got %+v, want %s", kind, finalizer, owner, want)
			}
		}
	}
}
