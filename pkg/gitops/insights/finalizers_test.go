package insights

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFinalizerOwnerMatchesWorkload(t *testing.T) {
	vm := &unstructured.Unstructured{}
	vm.SetAPIVersion("operator.victoriametrics.com/v1beta1")
	argo := &unstructured.Unstructured{}
	argo.SetAPIVersion("argoproj.io/v1alpha1")
	kustomization := &unstructured.Unstructured{}
	kustomization.SetAPIVersion("kustomize.toolkit.fluxcd.io/v1")
	renamed := map[string]string{"app.kubernetes.io/name": "vmop"}
	containers := func(image string, args ...string) corev1.PodTemplateSpec {
		return corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: renamed}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "operator", Image: image, Args: args}}}}
	}
	for _, tt := range []struct {
		name, finalizer, workload string
		root                      *unstructured.Unstructured
		labels                    map[string]string
		template                  corev1.PodTemplateSpec
		want                      bool
	}{
		{name: "AWS node class", finalizer: "karpenter.k8s.aws/termination", root: vm, workload: "karpenter", want: true},
		{name: "Azure node class", finalizer: "karpenter.azure.com/termination", root: vm, workload: "karpenter", want: true},
		{name: "karpenter deployment", finalizer: "karpenter.sh/termination", root: vm, workload: "karpenter", want: true},
		{name: "catalog label", finalizer: "apps.victoriametrics.com/finalizer", root: vm, workload: "custom", labels: map[string]string{"app.kubernetes.io/name": "victoria-metrics-operator"}, want: true},
		{name: "catalog name", finalizer: "apps.victoriametrics.com/finalizer", root: vm, workload: "vm-victoria-metrics-operator", want: true},
		{name: "renamed install by Docker Hub image", finalizer: "apps.victoriametrics.com/finalizer", root: vm, workload: "vmop", labels: renamed, template: containers("victoriametrics/operator:v0.50.0"), want: true},
		{name: "renamed install by mirrored image", finalizer: "apps.victoriametrics.com/finalizer", root: vm, workload: "vmop", labels: renamed, template: containers("mirror.example.com:5000/victoriametrics/operator:v0.50.0@sha256:0123"), want: true},
		{name: "mirror that changes the path", finalizer: "apps.victoriametrics.com/finalizer", root: vm, workload: "vmop", labels: renamed, template: containers("mirror.example.com/dockerhub/victoriametrics/operator:v0.50.0")},
		{name: "renamed install of another image", finalizer: "apps.victoriametrics.com/finalizer", root: vm, workload: "vmop", labels: renamed, template: containers("victoriametrics/vmagent:v1.100.0")},
		{name: "flux controller by ghcr image", finalizer: "finalizers.kustomize.toolkit.fluxcd.io", root: kustomization, workload: "kc", template: containers("ghcr.io/fluxcd/kustomize-controller:v1.4.0"), want: true},
		{name: "flux controller image of a sibling", finalizer: "finalizers.kustomize.toolkit.fluxcd.io", root: kustomization, workload: "hc", template: containers("ghcr.io/fluxcd/helm-controller:v1.1.0")},
		{name: "argo cd application controller by image and binary", finalizer: "resources-finalizer.argocd.argoproj.io", root: argo, workload: "ctrl", template: containers("quay.io/argoproj/argocd:v2.13.0", "/usr/local/bin/argocd-application-controller", "--metrics-port=8082"), want: true},
		{name: "argo cd server shares the image", finalizer: "resources-finalizer.argocd.argoproj.io", root: argo, workload: "web", template: containers("quay.io/argoproj/argocd:v2.13.0", "/usr/local/bin/argocd-server")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owner := ResolveFinalizerOwner(tt.finalizer, tt.root)
			if owner == nil {
				t.Fatal("expected a catalog owner")
			}
			workload := &metav1.ObjectMeta{Name: tt.workload, Labels: tt.labels}
			if got := owner.MatchesWorkload(workload, tt.template); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestImageRepositoryPath(t *testing.T) {
	for image, want := range map[string]string{
		"victoriametrics/operator":                                 "victoriametrics/operator",
		"victoriametrics/operator:v0.50.0":                         "victoriametrics/operator",
		"docker.io/victoriametrics/operator:v0.50.0":               "victoriametrics/operator",
		"ghcr.io/fluxcd/helm-controller:v1.1.0@sha256:abc":         "fluxcd/helm-controller",
		"localhost/fluxcd/helm-controller":                         "fluxcd/helm-controller",
		"registry:5000/fluxcd/helm-controller:v1":                  "fluxcd/helm-controller",
		"public.ecr.aws/karpenter/controller:1.0.0":                "karpenter/controller",
		"mcr.microsoft.com/aks/karpenter/controller:v1.0.0":        "aks/karpenter/controller",
		"mirror.example.com/dockerhub/victoriametrics/operator:v1": "dockerhub/victoriametrics/operator",
	} {
		if got := ImageRepositoryPath(image); got != want {
			t.Errorf("%s: got %q want %q", image, got, want)
		}
	}
}

// A first-label name match is too weak to identify an owner: it may only
// name a candidate.
func TestFinalizerControllerCandidate(t *testing.T) {
	root := &unstructured.Unstructured{}
	for _, tt := range []struct {
		name, group, finalizer, workload string
		labels                           map[string]string
		want                             bool
	}{
		{name: "name", group: "widgets.example.com", finalizer: "widgets.example.com/cleanup", workload: "release-widgets-operator", want: true},
		{name: "part-of", group: "widgets.example.com", finalizer: "widgets.example.com/cleanup", workload: "custom", labels: map[string]string{"app.kubernetes.io/part-of": "widgets-operator"}, want: true},
		{name: "unrelated domain", group: "other.example.com", finalizer: "widgets.example.com/cleanup", workload: "widgets-operator"},
		{name: "domain only", group: "widgets.example.com", finalizer: "widgets.example.com/cleanup", workload: "widgets-server"},
		{name: "substring", group: "widgets.example.com", finalizer: "widgets.example.com/cleanup", workload: "notwidgets-operator"},
		{name: "reserved domain", group: "k8s.io", finalizer: "k8s.io/cleanup", workload: "k8s-operator"},
		{name: "catalog finalizers are never candidates", group: "karpenter.sh", finalizer: "karpenter.sh/termination", workload: "karpenter-operator"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root.SetAPIVersion(tt.group + "/v1")
			workload := &metav1.ObjectMeta{Name: tt.workload, Labels: tt.labels}
			if got := FinalizerControllerCandidate(tt.finalizer, root, workload); got != tt.want {
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
			if FinalizerControllerCandidate(finalizer, root, &metav1.ObjectMeta{Name: workload}) {
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
