package prometheus

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	listerscorev1 "k8s.io/client-go/listers/core/v1"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/prom"
)

// Background rediscovery. Prewarm runs discovery when Radar connects to a
// cluster; after that, these cluster events start it again so the next caller
// finds a working connection instead of paying for discovery itself:
//
//   - a Service matching discovery's candidate rules is created or changes
//     shape while Radar has no connection (Prometheus installed later);
//   - a Pod behind a candidate Service turns Ready while Radar has no
//     connection (the Service usually exists before its Pod can answer), or
//     behind the connected Service (its backend was replaced);
//   - the port-forward serving the connection ends on its own (the Pod it
//     pointed at went away);
//   - a query against the connected endpoint fails without an HTTP response.
//
// Nothing runs on a timer. A run that a signal started and that failed against
// a candidate that exists gets a few follow-up attempts (rediscoveryFollowUps),
// because a Prometheus can turn Ready before it has scraped anything and the
// probe rejects an instance with no targets; no further event announces the
// first scrape.
//
// Each run is an ordinary EnsureConnected call, so the singleflight, discovery
// generation, supersession and failedDiscoveryCacheTTL rules apply unchanged.
// A manually configured URL is never replaced: discovery itself only ever
// probes it, and the cluster signals are ignored while one is set.

// rediscoveryDebounce coalesces a burst of signals (a chart install creates
// several Services and Pods at once) into one run.
var rediscoveryDebounce = 500 * time.Millisecond

// rediscoveryFollowUps are the delays before each follow-up attempt after a
// signal-started run fails against an existing candidate. They cover the
// first scrape of a Prometheus that just turned Ready (up to one default 1m
// scrape interval) and then stop.
var rediscoveryFollowUps = []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 60 * time.Second}

// backgroundConnectForTest replaces the run itself when set, so tests can
// observe runs without a cluster.
var backgroundConnectForTest func(c *Client, label string) error

// rediscoveryState is guarded by its own mutex, not Client.mu, so signals
// arriving on informer goroutines never wait on discovery bookkeeping.
type rediscoveryState struct {
	mu      sync.Mutex
	running bool // a worker goroutine owns the queue
	pending bool
	// wake interrupts the worker's wait when a signal arrives, so a new event
	// never sits behind a follow-up delay meant for an older one.
	wake chan struct{}
	// verifyAddr is the connected address a signal called into question;
	// empty when every pending signal only matters without a connection.
	verifyAddr string
	reason     string
	gen        uint64
}

// requestRediscovery queues a background run. verifyAddr names the connected
// address the signal is about (a failed query, a lost forward, a replaced
// backend); leave it empty for signals that only matter while Radar has no
// connection.
func (c *Client) requestRediscovery(verifyAddr, reason string) {
	r := &c.rediscovery
	r.mu.Lock()
	// Read the generation under the queue lock, so a request that raced a
	// Reset can never stamp an older generation over a newer queued signal.
	c.mu.RLock()
	retired := c.retired
	gen := c.discoveryGen
	c.mu.RUnlock()
	if retired {
		r.mu.Unlock()
		return
	}
	if !r.pending || r.gen != gen {
		r.verifyAddr = ""
	}
	// A signal questioning the connection outranks one that only matters
	// without it; otherwise the latest signal names the run.
	if verifyAddr != "" || r.verifyAddr == "" {
		r.reason = reason
	}
	if verifyAddr != "" {
		r.verifyAddr = verifyAddr
	}
	r.pending = true
	r.gen = gen
	if r.wake == nil {
		r.wake = make(chan struct{}, 1)
	}
	start := !r.running
	r.running = true
	wake := r.wake
	r.mu.Unlock()
	if start {
		go c.rediscoveryLoop(wake)
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

// rediscoveryLoop drains the queue and exits when it is empty. Only one runs
// per Client at a time.
func (c *Client) rediscoveryLoop(wake chan struct{}) {
	delay := rediscoveryDebounce
	followUp := 0
	signal := "" // the reason that started the current follow-up budget
	for {
		select {
		case <-time.After(delay):
		case <-wake:
			// A new signal: it gets its own debounce and a fresh budget
			// rather than the remainder of an older signal's follow-up wait.
			followUp = 0
			time.Sleep(rediscoveryDebounce)
		}
		c.waitForDiscoveryIdle()

		r := &c.rediscovery
		r.mu.Lock()
		if !r.pending {
			r.running = false
			r.mu.Unlock()
			return
		}
		verifyAddr, reason, gen := r.verifyAddr, r.reason, r.gen
		r.pending = false
		r.verifyAddr = ""
		r.mu.Unlock()
		if followUp == 0 {
			signal = reason
		}

		retry := c.runRediscovery(verifyAddr, reason, gen)

		r.mu.Lock()
		switch {
		case r.pending:
			// A new signal arrived during the run: it starts a fresh budget.
			delay, followUp = rediscoveryDebounce, 0
		case retry && followUp < len(rediscoveryFollowUps):
			delay = rediscoveryFollowUps[followUp]
			followUp++
			r.pending = true
			r.gen = gen
			r.reason = fmt.Sprintf("follow-up %d/%d after %s", followUp, len(rediscoveryFollowUps), signal)
		default:
			r.running = false
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
	}
}

// waitForDiscoveryIdle waits until no discovery run is in flight and any
// recent failure has aged past failedDiscoveryCacheTTL, rechecking after each
// wait because another caller may start a run or record a failure meanwhile.
// Every run ends within discoveryTimeout, so the wait is bounded.
func (c *Client) waitForDiscoveryIdle() {
	for {
		c.mu.RLock()
		done := c.flightDone
		failedAt := c.lastDiscoverAt
		failed := c.lastDiscoverErr != nil
		retired := c.retired
		c.mu.RUnlock()
		if retired {
			return
		}
		if done != nil {
			<-done
			continue
		}
		if failed && !failedAt.IsZero() {
			if wait := time.Until(failedAt.Add(failedDiscoveryCacheTTL)); wait > 0 {
				time.Sleep(wait)
				continue
			}
		}
		return
	}
}

// runRediscovery performs one queued run and reports whether a follow-up
// attempt is worthwhile.
func (c *Client) runRediscovery(verifyAddr, reason string, gen uint64) bool {
	c.mu.RLock()
	retired := c.retired
	currentGen := c.discoveryGen
	base := c.baseURL
	manual := c.manualURL != ""
	gated := c.headersRequireURLLocked()
	c.mu.RUnlock()
	switch {
	case retired, gated:
		return false
	case currentGen != gen:
		// The configuration changed after the signal (Reset, a new URL or
		// headers); whatever changed it owns the next discovery.
		return false
	case verifyAddr == "" && (base != "" || manual):
		// Connected since the signal, or a manual URL that cluster changes
		// cannot affect.
		return false
	case verifyAddr != "" && base != "" && base != verifyAddr:
		// Already replaced by a newer connection.
		return false
	}

	connect := (*Client).connectInBackground
	if backgroundConnectForTest != nil {
		connect = backgroundConnectForTest
	}
	err := connect(c, "Background rediscovery ("+reason+")")
	switch {
	case err == nil,
		errors.Is(err, ErrPrometheusNotFound),
		errors.Is(err, errDiscoverySuperseded),
		errors.Is(err, prom.ErrHeadersRequireURL),
		errors.Is(err, context.Canceled):
		// Connected, or nothing to retry: no candidate exists (a new Service
		// will signal again), or the run was superseded.
		return false
	}
	return !manual
}

// noteQueryFailure is called when a request to addr got no HTTP response
// while its caller was still waiting.
func (c *Client) noteQueryFailure(addr string, err error) {
	c.mu.RLock()
	current := c.baseURL == addr && !c.retired
	c.mu.RUnlock()
	if current {
		c.requestRediscovery(addr, fmt.Sprintf("query to %s failed: %v", prom.SafeAddress(addr), prom.RedactURLs(err.Error())))
	}
}

// onForwardLost handles a port-forward that ended on its own. When it served
// the connection, the connection is gone with it: drop it so no caller probes
// a dead local port, and rediscover in the background.
func (c *Client) onForwardLost(owner, address string) {
	// The callback runs after the forward registry unlocked; if a replacement
	// forward was published on the same local port meanwhile, the address is
	// live again and the loss describes the old tunnel only.
	if portforward.IsActiveAddress(address) {
		return
	}
	c.mu.Lock()
	if c.retired || c.baseURL != address {
		c.mu.Unlock()
		return
	}
	c.baseURL = ""
	c.basePath = ""
	c.prom = nil
	c.mu.Unlock()
	log.Printf("[prometheus] port-forward %s (owner=%s) serving the connection was lost, rediscovering in background", address, owner)
	c.requestRediscovery(address, "port-forward "+address+" lost")
}

// RegisterRediscoverySignals subscribes the process-wide client to the cluster
// signals above. Call once at startup; the subscriptions follow whichever
// client and resource cache are current when a signal fires.
func RegisterRediscoverySignals() {
	k8s.OnResourceObserved(observeResourceChange)
	portforward.SetOnForwardLost(func(owner portforward.Owner, address string) {
		if c := GetClient(); c != nil {
			c.onForwardLost(owner, address)
		}
	})
}

// observeResourceChange receives the resource cache's change stream and turns
// Prometheus-relevant changes into background rediscovery signals. It runs on
// informer goroutines, so it returns early for everything but Services and
// Pods and does no I/O.
func observeResourceChange(source kubernetes.Interface, change k8s.ResourceChange, obj, oldObj any) {
	if change.Kind != "Service" && change.Kind != "Pod" {
		return
	}
	if change.Operation != "add" && change.Operation != "update" {
		return
	}
	if change.Operation == "add" && !k8s.InitialSyncComplete() {
		// The initial listing: Prewarm's discovery covers what already exists.
		// Adds after it are objects Radar had not seen, whatever their age —
		// a relist after a watch outage included.
		return
	}
	c := GetClient()
	if c == nil {
		return
	}
	c.observeResourceChange(source, change.Operation, obj, oldObj)
}

func (c *Client) observeResourceChange(source kubernetes.Interface, op string, obj, oldObj any) {
	// Filter on the object first: this sees every Pod status write.
	var svc *corev1.Service
	var pod *corev1.Pod
	switch o := obj.(type) {
	case *corev1.Service:
		if !serviceBecameCandidate(op, o, oldObj) {
			return
		}
		svc = o
	case *corev1.Pod:
		if !podBecameReady(op, o, oldObj) {
			return
		}
		pod = o
	default:
		return
	}

	c.mu.RLock()
	sameCluster := c.k8sClient != nil && c.k8sClient == source
	skip := c.retired || c.manualURL != ""
	base := c.baseURL
	var connected *prom.ServiceInfo
	if base != "" && c.discoveryService != nil {
		cp := *c.discoveryService
		connected = &cp
	}
	c.mu.RUnlock()
	// A late event from a replaced cache describes another cluster.
	if !sameCluster || skip {
		return
	}

	if svc != nil {
		if base == "" {
			c.requestRediscovery("", fmt.Sprintf("service %s/%s %s", svc.Namespace, svc.Name, pastTense(op)))
		}
		return
	}
	if base != "" && connected == nil {
		return
	}
	backing := candidateServiceSelecting(c.cachedServiceLister(), pod, connected)
	if backing == nil {
		return
	}
	reason := fmt.Sprintf("pod %s/%s of service %s/%s became ready", pod.Namespace, pod.Name, backing.Namespace, backing.Name)
	if base != "" {
		c.requestRediscovery(base, reason)
		return
	}
	c.requestRediscovery("", reason)
}

func pastTense(op string) string {
	if op == "add" {
		return "created"
	}
	return "updated"
}

// serviceBecameCandidate reports whether a Service add or update is worth a
// discovery run: a new candidate, a Service that just became one, or a
// candidate whose ports or selector changed. Metadata churn on an existing
// candidate is not — it would rerun a failing discovery on every write.
func serviceBecameCandidate(op string, svc *corev1.Service, oldObj any) bool {
	if !prom.IsCandidateService(svc) {
		return false
	}
	if op == "add" {
		return true
	}
	old, ok := oldObj.(*corev1.Service)
	if !ok || !prom.IsCandidateService(old) {
		return true
	}
	return !portsEqual(old.Spec.Ports, svc.Spec.Ports) || !labels.Equals(old.Spec.Selector, svc.Spec.Selector)
}

func portsEqual(a, b []corev1.ServicePort) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Port != b[i].Port || a[i].TargetPort != b[i].TargetPort {
			return false
		}
	}
	return true
}

// podBecameReady reports a transition into Ready: an update from not-Ready,
// or a newly created Pod that is already Ready.
func podBecameReady(op string, pod *corev1.Pod, oldObj any) bool {
	if !podReady(pod) || pod.DeletionTimestamp != nil {
		return false
	}
	if op == "add" {
		return true
	}
	old, ok := oldObj.(*corev1.Pod)
	return ok && !podReady(old)
}

func podReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// candidateServiceSelecting returns a Service in the Pod's namespace that
// selects it and matters: the connected Service when there is one (its
// backend changed), otherwise any discovery candidate. lister is nil when the
// informer cache is not this client's cluster.
func candidateServiceSelecting(lister listerscorev1.ServiceLister, pod *corev1.Pod, connected *prom.ServiceInfo) *corev1.Service {
	if connected != nil && connected.Namespace != pod.Namespace {
		return nil
	}
	if lister == nil {
		return nil
	}
	svcs, err := lister.Services(pod.Namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	podLabels := labels.Set(pod.Labels)
	for _, svc := range svcs {
		if connected != nil && svc.Name != connected.Name {
			continue
		}
		if len(svc.Spec.Selector) == 0 || !labels.SelectorFromSet(svc.Spec.Selector).Matches(podLabels) {
			continue
		}
		if connected != nil || prom.IsCandidateService(svc) {
			return svc
		}
	}
	return nil
}

// failureReportingTransport tells the client when a request to the connected
// endpoint got no HTTP response while its caller was still waiting — the
// endpoint, or the forward in front of it, is likely gone or hung. That
// includes the HTTP client's own timeout. An HTTP error status, an unreadable
// body or the caller's own cancellation or deadline say nothing about
// reachability and are not reported.
type failureReportingTransport struct {
	prom.Transport
	addr   string
	report func(addr string, err error)
}

type skipFailureReportKey struct{}

// withoutFailureReport marks a request whose caller handles an unanswered
// endpoint itself — EnsureConnected's probe rediscovers on failure — so the
// transport does not also queue a background run for it.
func withoutFailureReport(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipFailureReportKey{}, true)
}

func (t failureReportingTransport) Do(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	body, err := t.Transport.Do(ctx, method, path, params)
	skip, _ := ctx.Value(skipFailureReportKey{}).(bool)
	if err != nil && !skip && ctx.Err() == nil && isNoResponseError(err) {
		t.report(t.addr, err)
	}
	return body, err
}

// isNoResponseError is checked only while the caller's context is live, so a
// context error here is the HTTP client's own timeout, which counts.
func isNoResponseError(err error) bool {
	var httpErr *prom.HTTPError
	var respErr *prom.ResponseError
	return !errors.As(err, &httpErr) && !errors.As(err, &respErr)
}

// newConnectedPromClientLocked builds a prom.Client for the connected endpoint
// whose transport reports unanswered requests. Caller holds c.mu.
func (c *Client) newConnectedPromClientLocked(httpClient *http.Client) *prom.Client {
	tr := prom.NewHTTPTransport(c.baseURL, c.basePath, httpClient)
	tr.Headers = copyHeaders(c.headers)
	return prom.NewClient(failureReportingTransport{Transport: tr, addr: c.baseURL, report: c.noteQueryFailure})
}

// cachedServiceLister returns the informer cache's Service lister when that
// cache was built from this client's cluster connection, else nil.
func (c *Client) cachedServiceLister() listerscorev1.ServiceLister {
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Client() != c.k8sClient {
		return nil
	}
	return cache.Services()
}

// cachedServices supplies prom.Discover with the cluster's Services from the
// informer cache. It declines (ok=false, so Discover asks the API) unless the
// cache is this client's cluster, watches Services in every namespace, and
// has finished syncing them — a namespace-scoped or partial view would
// silently drop candidates.
func (c *Client) cachedServices() ([]*corev1.Service, bool) {
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Client() != c.k8sClient {
		return nil, false
	}
	if !cache.IsKindClusterWide(string(k8score.Services)) || !cache.IsKindReady(string(k8score.Services)) {
		return nil, false
	}
	lister := cache.Services()
	if lister == nil {
		return nil, false
	}
	svcs, err := lister.List(labels.Everything())
	if err != nil {
		return nil, false
	}
	return svcs, true
}
