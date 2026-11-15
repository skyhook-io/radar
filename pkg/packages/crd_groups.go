package packages

// crdGroupToChart maps CRD spec.group → conventional chart name. Lets
// the merge logic collapse "cert-manager Helm release + cert-manager.io
// CRDs" into a single row instead of two parallel ones.
//
// Sourced from observed customer installs. Conservative: groups not in
// this map become standalone rows keyed by the group string itself
// (FromCRDGroup field set), so we don't lose visibility on operators
// we haven't seen before.
//
// The name is also the integration's name in usage reports and an entry in
// the add-on catalog (addons.go), which matches workload, label and chart
// names by it. Prefer the canonical Helm chart name, which is how package
// rows merge, unless it is generic enough to tag user apps as add-ons (KAITO's
// chart is "workspace"). A name that identifies an integration but must not
// classify workloads goes in integrationsNotAddons.
var crdGroupToChart = map[string]string{
	// Cert-manager
	"cert-manager.io":      "cert-manager",
	"acme.cert-manager.io": "cert-manager",

	// Argo CD + Argo Workflows + Argo Rollouts.
	// All three share the argoproj.io group; we map to "argo-cd" so
	// argo-rollouts standalone installs get folded in. Acceptable
	// because the chart-name match is an aggregation hint, not a strict
	// install identity.
	"argoproj.io": "argo-cd",

	// Flux v2 (toolkit)
	"source.toolkit.fluxcd.io":       "flux",
	"helm.toolkit.fluxcd.io":         "flux",
	"kustomize.toolkit.fluxcd.io":    "flux",
	"notification.toolkit.fluxcd.io": "flux",
	"image.toolkit.fluxcd.io":        "flux",

	// Karpenter
	"karpenter.sh":        "karpenter",
	"karpenter.k8s.aws":   "karpenter",
	"karpenter.azure.com": "karpenter",
	"karpenter.k8s.gcp":   "karpenter",

	// External Secrets Operator
	"external-secrets.io": "external-secrets",

	// Velero
	"velero.io": "velero",

	// Kyverno
	"kyverno.io":          "kyverno",
	"policies.kyverno.io": "kyverno",
	"reports.kyverno.io":  "kyverno",
	"wgpolicyk8s.io":      "kyverno", // PolicyReport CRDs

	// Prometheus stack (kube-prometheus-stack)
	"monitoring.coreos.com": "kube-prometheus-stack",

	// Istio
	"networking.istio.io": "istio",
	"security.istio.io":   "istio",
	"telemetry.istio.io":  "istio",
	"install.istio.io":    "istio",
	"extensions.istio.io": "istio",

	// Traefik
	"traefik.io":          "traefik",
	"traefik.containo.us": "traefik",

	// CloudNativePG, and its Barman Cloud backup plugin, a separate chart
	"postgresql.cnpg.io":  "cloudnative-pg",
	"barmancloud.cnpg.io": "plugin-barman-cloud",

	// Flagger
	"flagger.app": "flagger",

	// Secrets Store CSI Driver
	"secrets-store.csi.x-k8s.io": "secrets-store-csi-driver",

	// OpenTelemetry
	"opentelemetry.io": "opentelemetry-operator",

	// KEDA
	"keda.sh":          "keda",
	"eventing.keda.sh": "keda",

	// Knative
	"operator.knative.dev":            "knative-operator",
	"serving.knative.dev":             "knative-serving",
	"networking.internal.knative.dev": "knative-serving",
	"eventing.knative.dev":            "knative-eventing",
	"sources.knative.dev":             "knative-eventing",
	"messaging.knative.dev":           "knative-eventing",
	"flows.knative.dev":               "knative-eventing",

	// Cluster API
	"cluster.x-k8s.io":                "cluster-api",
	"controlplane.cluster.x-k8s.io":   "cluster-api",
	"bootstrap.cluster.x-k8s.io":      "cluster-api",
	"infrastructure.cluster.x-k8s.io": "cluster-api",
	"addons.cluster.x-k8s.io":         "cluster-api",

	// Trivy operator
	"aquasecurity.github.io": "trivy-operator",

	// Cilium
	"cilium.io": "cilium",

	// Calico installs through the tigera-operator chart
	"crd.projectcalico.org": "tigera-operator",
	"projectcalico.org":     "tigera-operator",
	"operator.tigera.io":    "tigera-operator",

	// Contour
	"projectcontour.io": "contour",

	// Gateway API and its inference extension
	"gateway.networking.k8s.io":     "gateway-api",
	"inference.networking.k8s.io":   "gateway-api-inference-extension",
	"inference.networking.x-k8s.io": "gateway-api-inference-extension",

	// Envoy Gateway; gateway-helm is its chart name
	"gateway.envoyproxy.io": "gateway-helm",

	// MetalLB
	"metallb.io": "metallb",

	// Bitnami Sealed Secrets
	"bitnami.com": "sealed-secrets",

	// OPA Gatekeeper
	"templates.gatekeeper.sh":    "gatekeeper",
	"constraints.gatekeeper.sh":  "gatekeeper",
	"config.gatekeeper.sh":       "gatekeeper",
	"mutations.gatekeeper.sh":    "gatekeeper",
	"status.gatekeeper.sh":       "gatekeeper",
	"expansion.gatekeeper.sh":    "gatekeeper",
	"externaldata.gatekeeper.sh": "gatekeeper",

	// Kubescape serves its reports through an aggregated API
	"spdx.softwarecomposition.kubescape.io": "kubescape-operator",

	// Crossplane
	"apiextensions.crossplane.io": "crossplane",
	"pkg.crossplane.io":           "crossplane",

	// Strimzi
	"kafka.strimzi.io": "strimzi-kafka-operator",
	"core.strimzi.io":  "strimzi-kafka-operator",

	// Tekton
	"tekton.dev": "tekton-pipelines",

	// Storage
	"longhorn.io":  "longhorn",
	"ceph.rook.io": "rook-ceph",

	// KubeVirt ships no canonical chart
	"kubevirt.io":              "kubevirt",
	"snapshot.kubevirt.io":     "kubevirt",
	"export.kubevirt.io":       "kubevirt",
	"instancetype.kubevirt.io": "kubevirt",
	"pool.kubevirt.io":         "kubevirt",
	"clone.kubevirt.io":        "kubevirt",
	"migrations.kubevirt.io":   "kubevirt",
	"backup.kubevirt.io":       "kubevirt",

	// GPU operators
	"nvidia.com":      "gpu-operator",
	"apps.nvidia.com": "k8s-nim-operator",
	"amd.com":         "gpu-operator-charts", // AMD's chart name

	// Batch, scheduling and model serving
	"kueue.x-k8s.io":           "kueue",
	"jobset.x-k8s.io":          "jobset",
	"leaderworkerset.x-k8s.io": "lws",
	"batch.volcano.sh":         "volcano",
	"scheduling.volcano.sh":    "volcano",
	"flow.volcano.sh":          "volcano",
	"bus.volcano.sh":           "volcano",
	"nodeinfo.volcano.sh":      "volcano",
	"topology.volcano.sh":      "volcano",
	"scheduling.run.ai":        "kai-scheduler",
	"ray.io":                   "kuberay-operator",
	"serving.kserve.io":        "kserve",
	"kubeflow.org":             "kubeflow",
	"trainer.kubeflow.org":     "kubeflow-trainer",
	"kaito.sh":                 "kaito",
	"llm-d.ai":                 "llm-d",
}

// chartlessIntegrations name APIs that no Helm chart installs. Usage stats
// still report them, but the package list treats their groups as unknown, so
// their CRDs keep the group as their identity instead of colliding with a
// user's release that shares the name.
var chartlessIntegrations = map[string]bool{
	"gateway-api": true,
}

// IntegrationForCRDGroup names the integration a served API group belongs to,
// using the same curated map as the package merge. Unknown groups return
// false and must not be reported under their own name: an unknown group can
// be a customer's internal API.
func IntegrationForCRDGroup(group string) (string, bool) {
	c, ok := crdGroupToChart[group]
	return c, ok
}

// chartFromCRDGroup returns (chartName, true) if the group is in our
// known mapping, else (group, false). Callers decide whether to render
// the group as a standalone row or skip.
func chartFromCRDGroup(group string) (string, bool) {
	if c, ok := crdGroupToChart[group]; ok && !chartlessIntegrations[c] {
		return c, true
	}
	return group, false
}
