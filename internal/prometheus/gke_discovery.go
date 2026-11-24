package prometheus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/pkg/prom"
)

const gmpFrontendImage = "gke.gcr.io/prometheus-engine/frontend"

var gkeIssuer = regexp.MustCompile(`^https://container\.googleapis\.com/v1/projects/([a-z0-9][a-z0-9.-]*)/locations/([a-z0-9-]+)/clusters/([a-z0-9-]+)$`)

func gkeFrontendCandidate(s *corev1.Service) bool {
	if s == nil || s.Spec.Type == corev1.ServiceTypeExternalName || len(s.Spec.Selector) == 0 || len(s.Spec.Ports) == 0 {
		return false
	}
	return prom.IsGMPFrontendLocation(s.Namespace, s.Name)
}

func gkeFrontendPort(s *corev1.Service, pods []corev1.Pod) (corev1.ServicePort, bool) {
	if len(pods) == 0 {
		return corev1.ServicePort{}, false
	}
	port := s.Spec.Ports[0]
	for _, p := range s.Spec.Ports {
		if p.Name == "web" {
			port = p
			break
		}
	}
	if port.Protocol != "" && port.Protocol != corev1.ProtocolTCP {
		return port, false
	}
	target := port.TargetPort.IntVal
	if port.TargetPort.StrVal == "" && target == 0 {
		target = port.Port
	}
	for _, p := range pods {
		matched := false
		for _, c := range p.Spec.Containers {
			if c.Image != gmpFrontendImage && !strings.HasPrefix(c.Image, gmpFrontendImage+":") && !strings.HasPrefix(c.Image, gmpFrontendImage+"@") {
				continue
			}
			if len(p.Spec.Containers) == 1 && port.TargetPort.StrVal == "" {
				matched = true
				break
			}
			for _, cp := range c.Ports {
				if cp.Protocol != "" && cp.Protocol != corev1.ProtocolTCP {
					continue
				}
				if (port.TargetPort.StrVal != "" && cp.Name == port.TargetPort.StrVal) || (target > 0 && cp.ContainerPort == target) {
					matched = true
					break
				}
			}
		}
		if !matched {
			return port, false
		}
	}
	return port, true
}

// The issuer comes from the connected API server. The kubeconfig context is a
// user-chosen alias and the frontend's project is its query scope, which can
// cover several projects. Neither identifies the cluster being charted.
func (c *Client) gkeClusterScope(ctx context.Context) (ClusterScope, string, bool) {
	cfg, _, configured := c.workloadMetricsConfig()
	l := maps.Clone(cfg.ClusterLabels)
	if l == nil {
		l = map[string]string{}
	}
	if cluster := l["cluster"]; cluster != "" {
		if other := l["cluster_name"]; other != "" && other != cluster {
			return ClusterScope{}, "", false
		}
		l["cluster_name"] = cluster
		delete(l, "cluster")
	}
	if c.k8sClient == nil || c.k8sConfig == nil {
		return ClusterScope{}, "", false
	}
	rc := c.k8sClient.Discovery().RESTClient()
	if rc == nil {
		return ClusterScope{}, "", false
	}
	b, err := rc.Get().AbsPath("/.well-known/openid-configuration").DoRaw(ctx)
	var doc struct {
		Issuer string `json:"issuer"`
	}
	if err == nil && json.Unmarshal(b, &doc) == nil {
		if m := gkeIssuer.FindStringSubmatch(doc.Issuer); m != nil {
			for key, value := range map[string]string{"project_id": m[1], "location": m[2], "cluster_name": m[3]} {
				if v, ok := l[key]; ok && v != value {
					return ClusterScope{}, "", false
				}
				l[key] = value
			}
		}
	}
	if l["project_id"] == "" || l["location"] == "" || l["cluster_name"] == "" {
		return ClusterScope{}, "", false
	}
	m, err := (prom.WorkloadMetricsScope{ClusterLabels: l}).Matchers()
	if err != nil {
		return ClusterScope{}, "", false
	}
	s := ClusterScope{State: ViewScopeVerified, Labels: l, Note: "GKE system metrics filtered to the connected cluster’s project, location and name."}
	if configured && len(cfg.ClusterLabels) > 0 {
		s.State = ViewScopeConfigured
		s.Note = "GKE system metrics filtered by the configured labels and cluster identity."
	}
	return s, m, true
}

func (c *Client) gkeCheckFreshLocked(gen uint64) bool {
	return c.gkeGeneration == gen && !c.gkeChecked.IsZero() && (c.gke == nil || c.gkeActiveLocked()) && time.Since(c.gkeChecked) < time.Minute
}

func (c *Client) gkeReader(ctx context.Context, gen uint64, regularErr error) *gkeMetrics {
	if ctx.Err() != nil {
		return nil
	}
	c.mu.RLock()
	fresh := c.gkeCheckFreshLocked(gen)
	revision := c.gkeRevision
	c.mu.RUnlock()
	if !fresh {
		ch := c.gkeSF.DoChan(fmt.Sprintf("%d/%d", gen, revision), func() (any, error) {
			c.findGKEChartStore(context.WithoutCancel(ctx), gen, regularErr)
			return nil, nil
		})
		select {
		case <-ctx.Done():
			return nil
		case <-ch:
		}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.discoveryGen != gen || !c.gkeActiveLocked() {
		return nil
	}
	return c.gke
}

func (c *Client) connectWithGKE(ctx context.Context) (string, string, error) {
	gen := c.DiscoveryGeneration()
	addr, path, err := c.EnsureConnected(ctx)
	c.gkeReader(ctx, gen, err)
	return addr, path, err
}

type chartStores struct {
	standard   boundStore
	native     *gkeMetrics
	supplement func(context.Context) (*gkeMetrics, error)
}

// Conventional reads do not wait for an optional native probe. A chart that
// needs it reuses the same discovery pass's Kubernetes Service inventory.
func (c *Client) chartStores(ctx context.Context) chartStores {
	if g := c.cachedNativeCharts(); g != nil {
		return chartStores{native: g}
	}
	gen := c.DiscoveryGeneration()
	standard, err := c.bindStore(ctx)
	if err != nil {
		return chartStores{standard: standard, native: c.gkeReader(ctx, gen, err)}
	}
	return chartStores{standard: standard, supplement: func(ctx context.Context) (*gkeMetrics, error) {
		if !c.ConnectionCurrent(standard.conn) {
			// A bound conventional check may already have withheld the chart.
			return nil, nil
		}
		g := c.gkeReader(ctx, standard.conn.generation, nil)
		if !c.ConnectionCurrent(standard.conn) {
			return nil, errDiscoverySuperseded
		}
		return g, ctx.Err()
	}}
}

func (c *Client) findGKEChartStore(ctx context.Context, gen uint64, regularErr error) {
	c.mu.RLock()
	configured := c.manualURL != ""
	fallback := c.baseURL != ""
	revision := c.gkeRevision
	operatorScope := c.workloadScope
	recent := c.retired || c.discoveryGen != gen || c.gkeCheckFreshLocked(gen)
	c.mu.RUnlock()
	if recent || c.k8sClient == nil || c.k8sConfig == nil || ctx.Err() != nil ||
		(configured && regularErr != nil) || (!configured && regularErr != nil && !errors.Is(regularErr, ErrPrometheusNotFound) && !IsUnreachable(regularErr)) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var result *gkeMetrics
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.discoveryGen == gen && c.gkeRevision == revision && c.workloadScope == operatorScope && !c.retired {
			if result != nil && c.gkeActiveLocked() && c.gke.matchers == result.matchers && c.gke.service == result.service && c.gke.portName == result.portName && c.gke.fallback == result.fallback && (!configured || c.gke.store == result.store) {
				result = c.gke
			}
			c.gke, c.gkeGeneration, c.gkeChecked = result, gen, time.Now()
		}
	}()
	var candidates []*corev1.Service
	if !configured {
		candidates = c.gkeServiceCandidates(ctx, gen, revision)
		if len(candidates) == 0 {
			return
		}
	}
	scope, m, ok := c.gkeClusterScope(ctx)
	if !ok {
		return
	}
	source := &gkeMetrics{scope: scope, matchers: m, owner: c, fallback: fallback}
	if configured {
		store, snap := c.PromSnapshot()
		if store == nil || !c.ConnectionCurrent(snap) {
			return
		}
		// A URL can serve both conventional Prometheus and GKE system metrics.
		// Each chart gives usable conventional series precedence.
		if !source.hasNodeMetrics(ctx, store) {
			return
		}
		source.configured = true
		source.store = store
		result = source
		return
	}
	for _, s := range candidates {
		pods, err := c.k8sClient.CoreV1().Pods(s.Namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(s.Spec.Selector).String()})
		if err != nil {
			continue
		}
		port, ok := gkeFrontendPort(s, pods.Items)
		if !ok {
			continue
		}
		var tr *prom.HTTPTransport
		if c.inCluster {
			host := net.JoinHostPort(s.Name+"."+s.Namespace+".svc.cluster.local", strconv.Itoa(int(port.Port)))
			hc := *c.httpClient
			hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			tr = prom.NewHTTPTransport("http://"+host, "", &hc)
		} else {
			// The API server authenticates the request to its own Service proxy.
			// Kubernetes bearer credentials never go to the frontend's address.
			base := strings.TrimRight(c.k8sConfig.Host, "/")
			h, err := rest.HTTPClientFor(c.k8sConfig)
			if err != nil {
				continue
			}
			hc := *h
			hc.Timeout = 10 * time.Second
			hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			service := s.Name
			if port.Name != "" {
				service += ":" + port.Name
			}
			path := fmt.Sprintf("/api/v1/namespaces/%s/services/%s/proxy", s.Namespace, service)
			tr = prom.NewHTTPTransport(base, path, &hc)
		}
		store := prom.NewClient(tr)
		if !source.hasNodeMetrics(ctx, store) {
			continue
		}
		source.service = prom.ServiceInfo{Namespace: s.Namespace, Name: s.Name, Port: int(port.Port)}
		source.portName = port.Name
		source.store = store
		result = source
		return
	}
}

func (g *gkeMetrics) hasNodeMetrics(ctx context.Context, store *prom.Client) bool {
	r, err := store.Query(ctx, "count("+g.series("k8s_node", "node_cpu_total_cores")+")")
	if err != nil || incompleteResult(r) || len(r.Annotations.Infos) > 0 || len(r.Series) != 1 {
		return false
	}
	v := firstValue(r)
	return v != nil && *v > 0 && !math.IsInf(*v, 0)
}

var gkeFrontendNames = []string{"gmp-frontend", "frontend"}

func (c *Client) gkeServiceCandidates(ctx context.Context, gen, revision uint64) []*corev1.Service {
	c.mu.RLock()
	inventory := c.gkeServices
	c.mu.RUnlock()
	if inventory == nil || inventory.generation != gen || inventory.revision != revision || time.Since(inventory.at) >= time.Minute {
		c.discoveryServices(ctx, gen)
		c.mu.RLock()
		inventory = c.gkeServices
		c.mu.RUnlock()
	}
	if inventory == nil || inventory.generation != gen || inventory.revision != revision {
		return nil
	}
	svcs := slices.Clone(inventory.services)
	if !inventory.complete {
		for _, ns := range []string{"gmp-public", "gmp-system", "gke-gmp-system"} {
			for _, name := range gkeFrontendNames {
				if s, err := c.k8sClient.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
					svcs = append(svcs, s)
				}
			}
		}
	}
	var candidates []*corev1.Service
	for _, s := range svcs {
		if gkeFrontendCandidate(s) {
			candidates = append(candidates, s)
		}
	}
	slices.SortFunc(candidates, func(a, b *corev1.Service) int {
		if (a.Name == "gmp-frontend") != (b.Name == "gmp-frontend") {
			if a.Name == "gmp-frontend" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
	})
	return candidates
}

// A native-only connection has no generic Prometheus base URL. Reuse the
// verified reader across a chart burst, then periodically retry discovery.
func (c *Client) cachedNativeCharts() *gkeMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.gkeActiveLocked() || c.gke.fallback || time.Since(c.gkeChecked) >= time.Minute {
		return nil
	}
	return c.gke
}

func (c *Client) gkeChartStore() *gkeMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.gkeActiveLocked() {
		return nil
	}
	return c.gke
}

func (c *Client) gkeActiveLocked() bool {
	if c.retired || c.gke == nil || c.gkeGeneration != c.discoveryGen {
		return false
	}
	if c.gke.configured {
		return c.manualURL != "" && c.baseURL != "" && c.gke.store == c.prom
	}
	return c.manualURL == "" && c.gke.fallback == (c.baseURL != "")
}

func (c *Client) invalidateGKEService(source kubernetes.Interface, op string, svc *corev1.Service, oldObj any) bool {
	old, _ := oldObj.(*corev1.Service)
	wasNative, nowNative := gkeFrontendCandidate(old), gkeFrontendCandidate(svc)
	nativeChange := (wasNative || nowNative) && (op != "update" || wasNative != nowNative || !portsEqual(old.Spec.Ports, svc.Spec.Ports) || !labels.Equals(old.Spec.Selector, svc.Spec.Selector))
	if nativeChange {
		c.invalidateGKECandidate(source, svc)
	}
	return nativeChange
}

func (c *Client) invalidateGKECandidate(source kubernetes.Interface, svc *corev1.Service) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.k8sClient != source || c.manualURL != "" || c.retired {
		return
	}
	c.gkeRevision++
	c.gkeChecked = time.Time{}
	if c.gke != nil && c.gke.service.Namespace == svc.Namespace && c.gke.service.Name == svc.Name {
		c.gke = nil
	}
}
