package packages

import "strings"

// The add-on catalog — the single Go-side source of truth for "is this 3rd-party
// platform machinery?". It is derived from two parts:
//
//  1. crdGroupToChart's canonical chart names — so any operator we already
//     recognize by its CRD group also classifies as an add-on, with zero extra
//     maintenance: add a CRD group, its chart joins the catalog automatically.
//  2. addonChartsExtra — platform/security charts that ship NO CRD group of
//     their own (ingress controllers, DNS, metrics, logging, secrets managers),
//     so (1) can't catch them.
//
// The Applications and Add-ons surfaces share this classifier rather than each
// carrying a private allowlist. The frontend's interim KNOWN_ADDON_CHARTS
// (radar-hub-web packagesModel.ts) mirrors this list until the hub forwards the
// classification on the wire; keep the two in sync until then.

// addonChartsExtra lists add-on charts not keyed by a crdGroupToChart entry.
// Kept in lockstep with radar-hub-web's KNOWN_ADDON_CHARTS extras.
var addonChartsExtra = []string{
	// Argo family beyond the argoproj.io→argo-cd catalog entry
	"argocd", "argo-rollouts", "argo-workflows", "argo-events", "flux2", "capi",
	// observability / logging / cost
	"prometheus", "prometheus-operator", "grafana", "loki", "tempo", "mimir",
	"victoria-metrics", "caretta", "alloy", "pyroscope", "promtail", "thanos",
	"jaeger", "kiali",
	"fluent-bit", "fluentd", "vector", "opencost", "kubecost",
	"kube-state-metrics", "node-problem-detector", "datadog", "newrelic-bundle",
	// service mesh / ingress / dns
	"istiod", "istio-base", "ingress-nginx", "nginx-ingress", "external-dns",
	"linkerd", "kong", "contour", "cilium",
	// gateway-helm is Envoy Gateway's actual chart name
	"envoy-gateway", "gateway-helm",
	// secrets / policy / security
	"sealed-secrets", "vault", "vault-secrets-operator", "gatekeeper",
	"falco", "trivy-operator",
	// autoscaling / scheduling / ops
	"metrics-server", "cluster-autoscaler", "vertical-pod-autoscaler", "vpa",
	"reloader", "descheduler", "aws-load-balancer-controller", "keda",
	"kured", "kube-fledged", "kubernetes-dashboard",
	// platform / storage / networking / serverless
	"cnpg", "cloudnative-pg", "opentelemetry-collector", "opentelemetry-operator",
	"crossplane", "crossplane-rbac-manager", "coredns", "calico", "longhorn",
	"metallb", "knative-serving", "knative-eventing", "knative-operator",
	"cluster-api",
}

// knownAddonCharts is the resolved catalog: every canonical chart name
// crdGroupToChart maps to, unioned with addonChartsExtra. Built once at init so
// the catalog stays a derivative of crdGroupToChart, not a parallel copy.
var knownAddonCharts = buildAddonCatalog()

// genericAddonNames are real add-ons, but common enough words that name-only
// evidence can misclassify a user app literally named "vector" or "vault".
// Chart/addon-manager/image evidence still classifies them.
var genericAddonNames = map[string]bool{
	"alloy": true, "contour": true, "kong": true, "vault": true, "vector": true,
}

var addonImageRepos = map[string][]string{
	"alloy":   {"grafana/alloy"},
	"contour": {"projectcontour/contour"},
	"kong":    {"kong/kubernetes-ingress-controller", "kong/kong-gateway", "kong/kong"},
	"vault":   {"hashicorp/vault", "hashicorp/vault-k8s", "hashicorp/vault-csi-provider"},
	"vector":  {"timberio/vector", "vectordotdev/vector"},
}

func buildAddonCatalog() map[string]bool {
	m := make(map[string]bool, len(crdGroupToChart)+len(addonChartsExtra))
	for _, chart := range crdGroupToChart {
		m[chart] = true
	}
	for _, chart := range addonChartsExtra {
		m[chart] = true
	}
	return m
}

// matchAddonChart reports the catalog entry a value matches — exact, or
// hyphen-prefixed so a versioned/sub-named value ("kube-prometheus-stack-45",
// "cert-manager-webhook") still hits its base. Returns ("", false) on no match.
func matchAddonChart(value string) (base string, ok bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return "", false
	}
	if knownAddonCharts[v] {
		return v, true
	}
	best := ""
	for known := range knownAddonCharts {
		if strings.HasPrefix(v, known+"-") {
			if len(known) > len(best) || (len(known) == len(best) && known < best) {
				best = known
			}
		}
	}
	if best != "" {
		return best, true
	}
	return "", false
}

func matchAddonImage(image string) (base string, ok bool) {
	repo := imageRepo(image)
	if repo == "" {
		return "", false
	}
	best := ""
	for addon, suffixes := range addonImageRepos {
		for _, suffix := range suffixes {
			if repo == suffix || strings.HasSuffix(repo, "/"+suffix) {
				if len(suffix) > len(best) || (len(suffix) == len(best) && suffix < best) {
					best = suffix
					base = addon
				}
			}
		}
	}
	return base, base != ""
}

func imageRepo(image string) string {
	v := strings.ToLower(strings.TrimSpace(image))
	if v == "" {
		return ""
	}
	if at := strings.Index(v, "@"); at >= 0 {
		v = v[:at]
	}
	slash := strings.LastIndex(v, "/")
	colon := strings.LastIndex(v, ":")
	if colon > slash {
		v = v[:colon]
	}
	return v
}

// ClassifyAddon reports whether a workload belongs to 3rd-party platform
// machinery, matching its helm.sh/chart base, primary image repo,
// app.kubernetes.io/{name,part-of}, or workload name against the shared add-on catalog. Returns the evidence
// ("field=value", with the matched catalog base in parens on a prefix hit) when
// classified.
//
// This is a CLASSIFIER, never a drop filter: callers keep the row visible
// (raw-always) and tag it, so the UI can fold add-ons away while still
// explaining WHY each was tagged.
func ClassifyAddon(chart, appName, partOf, name, addonManagerMode, image string) (addon bool, why string) {
	// The upstream addon-manager label is the platform itself declaring
	// "managed addon" (GKE stamps it on managed components, upstream
	// addon-manager on its manifests) — stronger than any catalog match, and
	// no user app carries it.
	if v := strings.TrimSpace(addonManagerMode); v != "" {
		return true, "addonmanager.kubernetes.io/mode=" + v
	}
	chartBase, _ := splitChart(chart)
	if addon, why := classifyAddonValue("chart", chartBase, true); addon {
		return addon, why
	}
	if base, ok := matchAddonImage(image); ok {
		return true, "image=" + imageRepo(image) + " (" + base + ")"
	}
	candidates := []struct{ field, value string }{
		{"app.kubernetes.io/name", appName},
		{"app.kubernetes.io/part-of", partOf},
		{"name", name},
	}
	for _, c := range candidates {
		if addon, why := classifyAddonValue(c.field, c.value, false); addon {
			return addon, why
		}
	}
	return false, ""
}

func classifyAddonValue(field, value string, strong bool) (bool, string) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return false, ""
	}
	base, ok := matchAddonChart(v)
	if !ok || (!strong && genericAddonNames[base]) {
		return false, ""
	}
	if base == v {
		return true, field + "=" + v
	}
	return true, field + "=" + v + " (" + base + ")"
}
