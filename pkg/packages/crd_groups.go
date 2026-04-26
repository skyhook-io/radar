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
// When adding a new group, also consider whether the chart name we use
// matches the canonical Helm chart name (the one that would appear in
// Helm release secrets) — that's how rows merge.
var crdGroupToChart = map[string]string{
	// Cert-manager
	"cert-manager.io":   "cert-manager",
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
	"karpenter.sh":      "karpenter",
	"karpenter.k8s.aws": "karpenter",

	// External Secrets Operator
	"external-secrets.io":          "external-secrets",

	// Velero
	"velero.io": "velero",

	// Kyverno
	"kyverno.io":                "kyverno",
	"wgpolicyk8s.io":            "kyverno", // PolicyReport CRDs

	// Prometheus stack (kube-prometheus-stack)
	"monitoring.coreos.com": "kube-prometheus-stack",

	// Istio
	"networking.istio.io":  "istio",
	"security.istio.io":    "istio",
	"telemetry.istio.io":   "istio",
	"install.istio.io":     "istio",
	"extensions.istio.io":  "istio",

	// Traefik
	"traefik.io":          "traefik",
	"traefik.containo.us": "traefik",

	// CloudNativePG
	"postgresql.cnpg.io": "cloudnative-pg",

	// OpenTelemetry
	"opentelemetry.io": "opentelemetry-operator",

	// KEDA
	"keda.sh": "keda",
	"eventing.keda.sh": "keda",

	// Knative
	"operator.knative.dev": "knative-operator",
	"serving.knative.dev":  "knative-serving",
	"eventing.knative.dev": "knative-eventing",
	"sources.knative.dev":  "knative-eventing",

	// Cluster API
	"cluster.x-k8s.io":           "cluster-api",
	"controlplane.cluster.x-k8s.io": "cluster-api",
	"bootstrap.cluster.x-k8s.io":  "cluster-api",
	"infrastructure.cluster.x-k8s.io": "cluster-api",
	"addons.cluster.x-k8s.io":     "cluster-api",

	// Trivy operator
	"aquasecurity.github.io": "trivy-operator",

	// Cilium
	"cilium.io":          "cilium",
}

// chartFromCRDGroup returns (chartName, true) if the group is in our
// known mapping, else (group, false). Callers decide whether to render
// the group as a standalone row or skip.
func chartFromCRDGroup(group string) (string, bool) {
	if c, ok := crdGroupToChart[group]; ok {
		return c, true
	}
	return group, false
}
