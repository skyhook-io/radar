package prom

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// wellKnownConcurrency bounds the parallel Service.Get fan-out over the
// well-known locations so discovery doesn't open a burst of connections to the
// API server at once.
const wellKnownConcurrency = 8

// CandidateSource describes how a candidate was found.
type CandidateSource string

const (
	CandidateSourceWellKnown CandidateSource = "well_known"
	CandidateSourceDynamic   CandidateSource = "dynamic"
)

// Candidate is a Prometheus-compatible service the caller can attempt to
// reach. Discover populates the fields and orders candidates by priority,
// but does not probe — it leaves the transport choice (direct HTTP vs.
// port-forward vs. tunneled proxy) to the caller.
type Candidate struct {
	Namespace   string
	Name        string
	Port        int             // service port (for in-cluster addressing)
	TargetPort  int             // container port (for port-forwarding to the pod)
	ClusterAddr string          // http://{name}.{ns}.svc.cluster.local:{port}
	BasePath    string          // e.g. "/select/0/prometheus" for vmselect
	Score       int             // relative likelihood of being Prometheus
	Source      CandidateSource // well_known | dynamic
}

// DiscoverOptions tunes Discover's behavior.
type DiscoverOptions struct {
	// IncludeDynamic controls whether a cluster-wide service scan is performed.
	// The scan is an O(all services) List call plus a scoring pass; skip it
	// for callers that only need a quick well-known check.
	IncludeDynamic bool

	// MaxDynamic caps the number of dynamic candidates returned. Default 5.
	MaxDynamic int

	// Logger is optional; if set, Discover emits verbose progress messages.
	Logger func(format string, args ...interface{})
}

// WellKnownLocations is the ordered list of namespaces + service names where
// Prometheus-compatible services are commonly installed.
var WellKnownLocations = []struct {
	Namespace string
	Name      string
	Port      int    // 0 = use service's first port
	BasePath  string // sub-path for Prometheus API
}{
	// VictoriaMetrics — monitoring namespace first (workload metrics)
	{"monitoring", "victoria-metrics-victoria-metrics-single-server", 8428, ""},
	{"monitoring", "victoria-metrics-single-server", 8428, ""},
	{"monitoring", "vmsingle", 8428, ""},
	{"monitoring", "vmselect", 8481, "/select/0/prometheus"},
	{"victoria-metrics", "victoria-metrics-victoria-metrics-single-server", 8428, ""},
	{"victoria-metrics", "victoria-metrics-single-server", 8428, ""},
	{"victoria-metrics", "vmsingle", 8428, ""},
	{"victoria-metrics", "vmselect", 8481, "/select/0/prometheus"},
	// kube-prometheus-stack
	{"monitoring", "kube-prometheus-stack-prometheus", 9090, ""},
	{"monitoring", "prometheus-kube-prometheus-prometheus", 9090, ""},
	{"monitoring", "prometheus-operated", 9090, ""},
	// Standard Prometheus
	{"opencost", "prometheus-server", 0, ""},
	{"monitoring", "prometheus-server", 0, ""},
	{"prometheus", "prometheus-server", 0, ""},
	{"observability", "prometheus-server", 0, ""},
	{"metrics", "prometheus-server", 0, ""},
	{"kube-system", "prometheus", 0, ""},
	{"default", "prometheus", 0, ""},
	// VictoriaMetrics — caretta namespace (traffic-specific, may lack workload metrics)
	{"caretta", "caretta-vm", 8428, ""},
}

// metricsNamespaces are commonly used for metrics services; used as a scoring
// signal in dynamic discovery.
var metricsNamespaces = map[string]bool{
	"monitoring":       true,
	"prometheus":       true,
	"observability":    true,
	"metrics":          true,
	"victoria-metrics": true,
	"caretta":          true,
	"opencost":         true,
}

// skipNamespaces are excluded from dynamic discovery.
var skipNamespaces = map[string]bool{
	"kube-public":     true,
	"kube-node-lease": true,
}

// nonQueryComponents are metrics-adjacent services that are commonly named or
// namespaced like Prometheus but do NOT serve the Prometheus HTTP query API
// (/api/v1/query). Discovery must never treat them as candidates: each one
// costs a serial port-forward attempt that can only ever fail. Matched as a
// substring against the service name and the app/name/component labels, so
// e.g. "kube-prometheus-stack-prometheus-node-exporter" is excluded by
// "node-exporter" even though it also contains "prometheus".
var nonQueryComponents = []string{
	"node-exporter",
	"kube-state-metrics",
	"state-metrics",
	"alertmanager",
	"pushgateway",
	"prometheus-adapter",
	"grafana",
	"blackbox",
	"thanos-compact",
	"thanos-rule", // covers thanos-ruler
	"thanos-store",
	"thanos-sidecar",
	"thanos-receive",
	// Control-plane scrape-target Services created by kube-prometheus-stack:
	// they carry "prometheus" in the name but only proxy a component's /metrics,
	// they don't serve the query API.
	"kubelet",
	"kube-etcd",
	"kube-scheduler",
	"kube-controller-manager",
	"kube-proxy",
	"coredns",
	"kube-dns",
}

// queryEngineAppNames / queryEngineAppLabels are definitive identities of a
// Prometheus-family query engine. When one is present, the substring deny-list
// below must not fire — a real endpoint (e.g. a chart installed with release
// name "grafana", giving service name "grafana-prometheus-server", but
// app.kubernetes.io/name=prometheus) is a query API despite the generic token.
var queryEngineAppNames = map[string]bool{
	"prometheus": true, "victoria-metrics-single": true, "vmsingle": true,
	"vmselect": true, "thanos-query": true, "thanos-querier": true,
}
var queryEngineAppLabels = map[string]bool{
	"prometheus": true, "prometheus-server": true, "vmsingle": true, "vmselect": true,
}

// isNonQueryService reports whether a service is a known non-query metrics
// component (exporter, operator, alertmanager, …) that cannot answer PromQL.
func isNonQueryService(svc corev1.Service) bool {
	labels := svc.Labels
	name := strings.ToLower(svc.Name)
	appName := strings.ToLower(labels["app.kubernetes.io/name"])
	component := strings.ToLower(labels["app.kubernetes.io/component"])

	// A definitive query-engine identity wins over any name substring.
	if queryEngineAppNames[appName] || queryEngineAppLabels[strings.ToLower(labels["app"])] {
		return false
	}

	// The Prometheus operator is a control plane, not a query API. Match it by
	// name suffix or exact label rather than a substring, so a real Prometheus
	// whose name merely contains "operator" — e.g. "prometheus-operator-prometheus"
	// from a release named "prometheus-operator" — is not excluded.
	if strings.HasSuffix(name, "operator") || appName == "prometheus-operator" || component == "prometheus-operator" {
		return true
	}

	haystack := []string{name, appName, component, strings.ToLower(labels["app"])}
	for _, token := range nonQueryComponents {
		for _, h := range haystack {
			if h != "" && strings.Contains(h, token) {
				return true
			}
		}
	}
	return false
}

// Discover enumerates candidate Prometheus-compatible services reachable to
// the given k8sClient. Well-known locations are returned first in declared
// priority order, optionally followed by dynamically-discovered services
// ranked by ScoreService.
//
// Discover does NOT probe any candidate — callers decide how to reach each
// (direct HTTP, port-forward, tunneled proxy) and then use
// pkg/prom.Client.Probe to validate.
func Discover(ctx context.Context, k8sClient kubernetes.Interface, opts DiscoverOptions) ([]Candidate, error) {
	if k8sClient == nil {
		return nil, fmt.Errorf("prom.Discover: k8sClient is nil")
	}
	if opts.MaxDynamic <= 0 {
		opts.MaxDynamic = 5
	}
	logf := opts.Logger
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}

	var out []Candidate

	// seen de-duplicates candidates by namespace/name/port/basePath so a
	// service reachable via both the well-known list and the dynamic scan is
	// probed and port-forwarded only once. Well-known wins ties because it is
	// appended first.
	seen := map[string]bool{}

	// Layer 1: well-known locations. Fetch them concurrently — this is ~20
	// targeted Gets, which run serially would dominate discovery latency against
	// a remote API server — then assemble in declared order for deterministic
	// priority. Targeted Gets (not a cluster-wide List) keep this working for
	// callers with get-but-not-list on Services.
	found := make([]*corev1.Service, len(WellKnownLocations))
	getErrs := make([]error, len(WellKnownLocations))
	sem := make(chan struct{}, wellKnownConcurrency)
	var wg sync.WaitGroup
	for i, loc := range WellKnownLocations {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			svc, err := k8sClient.CoreV1().Services(loc.Namespace).Get(ctx, loc.Name, metav1.GetOptions{})
			if err != nil {
				getErrs[i] = err
				return
			}
			found[i] = svc
		})
	}
	wg.Wait()

	// Assemble in declared order, logging serially — DiscoverOptions.Logger has
	// no concurrency contract, so it must not be called from the workers above.
	for i, loc := range WellKnownLocations {
		svc := found[i]
		if svc == nil {
			if err := getErrs[i]; err != nil && !apierrors.IsNotFound(err) {
				logf("prom.Discover: error checking %s/%s: %v", loc.Namespace, loc.Name, err)
			}
			continue
		}
		port := resolvePort(*svc, loc.Port)
		key := candidateKey(svc.Namespace, svc.Name, port, loc.BasePath)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Candidate{
			Namespace:   svc.Namespace,
			Name:        svc.Name,
			Port:        port,
			TargetPort:  resolveTargetPort(*svc, port),
			ClusterAddr: buildClusterAddr(svc.Name, svc.Namespace, svc.Spec.ClusterIP, port),
			BasePath:    loc.BasePath,
			Source:      CandidateSourceWellKnown,
		})
	}

	if !opts.IncludeDynamic {
		return out, nil
	}

	// Layer 2: dynamic cluster-wide scan. The score only *ranks* candidates; it
	// is not an admission gate. Anything we're certain can't answer PromQL is
	// already excluded by ScoreService (isNonQueryService), and the caller's
	// probe is the authoritative check — so a weak but positive score is kept
	// and ranked low rather than dropped, which would silently miss a real but
	// lightly-labeled Prometheus. MaxDynamic still bounds how many are returned.
	svcs, err := k8sClient.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		logf("prom.Discover: failed to list services: %v", err)
		return out, nil // well-known results still useful
	}

	var scored []Candidate
	for _, svc := range svcs.Items {
		score, bp, identity := ScoreService(svc)
		// Admit on a Prometheus-family identity signal, not on score alone: a
		// namespace-only match (score without identity) would send the operator's
		// configured Prometheus headers to an unrelated neighbor when probed.
		if !identity {
			continue
		}
		port := resolvePort(svc, 0)
		if seen[candidateKey(svc.Namespace, svc.Name, port, bp)] {
			continue
		}
		scored = append(scored, Candidate{
			Namespace:   svc.Namespace,
			Name:        svc.Name,
			Port:        port,
			TargetPort:  resolveTargetPort(svc, port),
			ClusterAddr: buildClusterAddr(svc.Name, svc.Namespace, svc.Spec.ClusterIP, port),
			BasePath:    bp,
			Score:       score,
			Source:      CandidateSourceDynamic,
		})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	if len(scored) > opts.MaxDynamic {
		scored = scored[:opts.MaxDynamic]
	}
	return append(out, scored...), nil
}

// promPortScore returns the heuristic weight for a port number matching a
// well-known Prometheus-family default (0 for anything else).
func promPortScore(port int32) int {
	switch port {
	case 9090: // Prometheus default
		return 30
	case 8428: // VictoriaMetrics single-node default
		return 30
	case 8481: // VictoriaMetrics vmselect default
		return 25
	case 9009, 10902: // Thanos Query (gRPC-era default; common HTTP port)
		return 25
	}
	return 0
}

// ScoreService computes a heuristic score for a service being
// Prometheus-compatible. It returns the ranking score, an inferred BasePath for
// vmselect-style services, and whether the service carries a Prometheus-family
// *identity* signal (a name/label/port match). identity gates admission:
// namespace membership alone contributes to the score for ranking but must not,
// on its own, make an unrelated Service a candidate — discovery probes
// candidates with the operator's configured Prometheus headers, so an
// unrecognized neighbor must not be admitted just for sharing a namespace.
func ScoreService(svc corev1.Service) (score int, basePath string, identity bool) {
	if svc.Spec.Type == corev1.ServiceTypeExternalName {
		return 0, "", false
	}
	if skipNamespaces[svc.Namespace] {
		return 0, "", false
	}
	if isNonQueryService(svc) {
		return 0, "", false
	}

	labels := svc.Labels
	appName := labels["app.kubernetes.io/name"]
	appLabel := labels["app"]
	component := labels["app.kubernetes.io/component"]

	switch appName {
	case "prometheus":
		score += 100
	case "victoria-metrics-single", "vmsingle":
		score += 100
	case "vmselect":
		score += 90
		basePath = "/select/0/prometheus"
	case "thanos-query", "thanos-querier":
		score += 80
	}

	switch appLabel {
	case "prometheus", "prometheus-server":
		score += 80
	case "vmsingle":
		score += 80
	case "vmselect":
		score += 80
		basePath = "/select/0/prometheus"
	}

	if score > 0 && component == "server" {
		score += 20
	}

	for _, p := range svc.Spec.Ports {
		// Credit a recognized port on either the service port or the container
		// targetPort — a real Prometheus is often fronted by a service port of
		// 80/http with targetPort 9090 — but only once per port entry.
		if s := promPortScore(p.Port); s > 0 {
			score += s
		} else {
			score += promPortScore(p.TargetPort.IntVal)
		}
		if strings.Contains(strings.ToLower(p.Name), "prometheus") {
			score += 10
		}
	}

	nameLower := strings.ToLower(svc.Name)
	if strings.Contains(nameLower, "prometheus") {
		score += 20
	}
	if strings.Contains(nameLower, "victoria") || strings.Contains(nameLower, "vmsingle") || strings.Contains(nameLower, "vmselect") {
		score += 20
		if strings.Contains(nameLower, "vmselect") && basePath == "" {
			basePath = "/select/0/prometheus"
		}
	}
	if strings.Contains(nameLower, "thanos") {
		score += 15
	}

	// Everything above is a Prometheus-family identity signal; the namespace
	// bonus below only nudges ranking and must not admit on its own.
	identity = score > 0

	if metricsNamespaces[svc.Namespace] {
		score += 10
	}

	return score, basePath, identity
}

// candidateKey identifies a candidate by the tuple that determines where and
// how it is probed, so the same service surfaced by both discovery layers is
// de-duplicated.
func candidateKey(namespace, name string, port int, basePath string) string {
	return fmt.Sprintf("%s/%s:%d%s", namespace, name, port, basePath)
}

func resolvePort(svc corev1.Service, defaultPort int) int {
	if defaultPort != 0 {
		return defaultPort
	}
	if len(svc.Spec.Ports) > 0 {
		return int(svc.Spec.Ports[0].Port)
	}
	return 80
}

// resolveTargetPort returns the container port, for port-forwarding which
// bypasses the Service. When the service port differs from the container's
// targetPort (e.g., service:80 → container:9090), port-forward needs the
// container port.
func resolveTargetPort(svc corev1.Service, servicePort int) int {
	for _, p := range svc.Spec.Ports {
		if int(p.Port) == servicePort {
			if p.TargetPort.IntVal > 0 {
				return int(p.TargetPort.IntVal)
			}
			return servicePort
		}
	}
	return servicePort
}

// buildClusterAddr returns the in-cluster HTTP URL for a service. Headless
// services (ClusterIP=None) use a pod-0 hostname; this is best-effort and
// really meant for stateful Prometheus deployments with predictable names.
func buildClusterAddr(name, namespace, clusterIP string, port int) string {
	if clusterIP == "None" {
		return fmt.Sprintf("http://%s-0.%s.%s.svc.cluster.local:%d", name, name, namespace, port)
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", name, namespace, port)
}
