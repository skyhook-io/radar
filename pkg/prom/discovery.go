package prom

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

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

	// Layer 1: well-known locations. Preserve declared order for determinism.
	for _, loc := range WellKnownLocations {
		svc, err := k8sClient.CoreV1().Services(loc.Namespace).Get(ctx, loc.Name, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				logf("prom.Discover: error checking %s/%s: %v", loc.Namespace, loc.Name, err)
			}
			continue
		}
		port := resolvePort(*svc, loc.Port)
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

	// Layer 2: dynamic cluster-wide scan, scored + sorted.
	svcs, err := k8sClient.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		logf("prom.Discover: failed to list services: %v", err)
		return out, nil // well-known results still useful
	}

	var scored []Candidate
	for _, svc := range svcs.Items {
		score, bp := ScoreService(svc)
		if score <= 0 {
			continue
		}
		port := resolvePort(svc, 0)
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

// ScoreService computes a heuristic score for a service being
// Prometheus-compatible. Returns the score and an inferred BasePath for
// vmselect-style services.
func ScoreService(svc corev1.Service) (score int, basePath string) {
	if svc.Spec.Type == corev1.ServiceTypeExternalName {
		return 0, ""
	}
	if skipNamespaces[svc.Namespace] {
		return 0, ""
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
		switch p.Port {
		case 9090: // Prometheus default
			score += 30
		case 8428: // VictoriaMetrics single-node default
			score += 30
		case 8481: // VictoriaMetrics vmselect default
			score += 25
		case 9009: // Thanos Query default
			score += 25
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

	if metricsNamespaces[svc.Namespace] {
		score += 10
	}

	return score, basePath
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
