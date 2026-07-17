package k8score

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

var ErrResourceNotFound = errors.New("resource not found")
var ErrResourceCountUnavailable = errors.New("resource count unavailable")

const directCountProbeLimit int64 = 2

// informerKey identifies one informer. ns == "" means a cluster-wide watch;
// a non-empty ns is a namespace-scoped watch. A GVR can have one cluster-wide
// informer OR several namespace-scoped ones (for users denied cluster-wide
// list but allowed in specific namespaces) — never both, since the
// cluster-wide informer already covers every namespace.
type informerKey struct {
	gvr schema.GroupVersionResource
	ns  string
}

// informerEntry is one running informer plus its lifecycle handle. synced is
// guarded by DynamicResourceCache.mu.
type informerEntry struct {
	informer cache.SharedIndexInformer
	cancel   context.CancelFunc
	synced   bool
}

// DynamicResourceCache provides on-demand caching for CRDs and other dynamic
// resources. It is safe for concurrent use. Application-specific callbacks
// (timeline, metrics) are injected via DynamicCacheConfig.
type DynamicResourceCache struct {
	factory     dynamicinformer.DynamicSharedInformerFactory
	nsFactories map[string]dynamicinformer.DynamicSharedInformerFactory // one per watched namespace, lazily created
	informers   map[informerKey]*informerEntry
	// fallbackResolved marks GVRs whose "all namespaces" scope was already
	// probed and fanned out across the fallback namespaces. Without it every
	// all-namespaces read of a fallback-scoped GVR would re-probe cluster-wide
	// plus each candidate namespace. Guarded by mu.
	fallbackResolved map[schema.GroupVersionResource]bool
	stopCh           chan struct{} // global shutdown; parent of every per-informer context
	stopOnce         sync.Once
	stopped          bool // set under mu by Stop; startWatching refuses new informers after
	mu               sync.RWMutex
	config           DynamicCacheConfig
	discoveryStatus  CRDDiscoveryStatus
	discoveryMu      sync.RWMutex
	discoveryDone    chan struct{} // closed when DiscoverAllCRDs() completes

	// gvrHandlers holds change handlers registered via AddGVRChangeHandler,
	// keyed by GVR. They are re-applied to every informer started for that GVR
	// — including namespace-scoped informers created lazily after registration
	// — so derived caches keep receiving events. Guarded by mu.
	gvrHandlers map[schema.GroupVersionResource][]cache.ResourceEventHandler

	// watchStarts collapses concurrent ensureWatching calls for the same
	// (gvr, ns) into one probe + informer start. The access probe runs
	// outside d.mu, so without this a parallel fan-out over N objects of an
	// unwatched kind would issue N redundant limit=1 list probes before one
	// caller wins startWatching.
	watchStarts singleflight.Group

	// CRD discovery completion callbacks
	crdCallbacks   []func()
	crdCallbacksMu sync.RWMutex
}

// NewDynamicResourceCache creates a dynamic resource cache with the given config.
func NewDynamicResourceCache(cfg DynamicCacheConfig) (*DynamicResourceCache, error) {
	if cfg.DynamicClient == nil {
		return nil, fmt.Errorf("dynamic client must not be nil")
	}
	if cfg.NamespaceScoped && cfg.Namespace == "" {
		return nil, fmt.Errorf("namespace must be set when NamespaceScoped is true")
	}

	factory := dynamicinformer.NewDynamicSharedInformerFactory(
		cfg.DynamicClient,
		0, // no resync — updates come via watch
	)
	if cfg.NamespaceScoped && cfg.Namespace != "" {
		log.Printf("Using namespace-scoped dynamic informers for namespace %q", cfg.Namespace)
	} else if len(cfg.NamespaceFallbacks) > 0 {
		log.Printf("Using namespace fallbacks for dynamic informers: %q", cfg.NamespaceFallbacks)
	} else if cfg.NamespaceFallback != "" {
		log.Printf("Using namespace fallback for dynamic informers: %q", cfg.NamespaceFallback)
	}

	d := &DynamicResourceCache{
		factory:          factory,
		nsFactories:      make(map[string]dynamicinformer.DynamicSharedInformerFactory),
		fallbackResolved: make(map[schema.GroupVersionResource]bool),
		informers:        make(map[informerKey]*informerEntry),
		stopCh:           make(chan struct{}),
		config:           cfg,
		discoveryStatus:  CRDDiscoveryIdle,
		discoveryDone:    make(chan struct{}),
		gvrHandlers:      make(map[schema.GroupVersionResource][]cache.ResourceEventHandler),
	}

	log.Println("Dynamic resource cache initialized")
	return d, nil
}

// factoryForNs returns the informer factory for ns, creating and caching a
// namespace-filtered factory on first use. ns == "" is the cluster-wide
// factory. Caller must hold d.mu.
func (d *DynamicResourceCache) factoryForNs(ns string) dynamicinformer.DynamicSharedInformerFactory {
	if ns == "" {
		return d.factory
	}
	if f, ok := d.nsFactories[ns]; ok {
		return f
	}
	f := dynamicinformer.NewFilteredDynamicSharedInformerFactory(d.config.DynamicClient, 0, ns, nil)
	d.nsFactories[ns] = f
	return f
}

// ---------------------------------------------------------------------------
// EnsureWatching / startWatching / probeAccess
// ---------------------------------------------------------------------------

// EnsureWatching starts watching a resource type if not already watching.
// The sync happens asynchronously — callers should use WaitForSync if they need to wait.
func (d *DynamicResourceCache) EnsureWatching(gvr schema.GroupVersionResource) error {
	return d.ensureWatching(gvr, "")
}

// ensureWatching guarantees an informer covering preferredNS exists for gvr.
// preferredNS == "" means "any/all namespaces": probe cluster-wide and, if
// denied, fall back to the configured NamespaceFallback. A non-empty
// preferredNS is a specific request: a cluster-wide informer (if the identity
// can list cluster-wide) covers it; otherwise we watch that one namespace.
// This is what lets a namespace-restricted user read a CRD in the namespaces
// they actually have access to, instead of being pinned to a single fallback.
func (d *DynamicResourceCache) ensureWatching(gvr schema.GroupVersionResource, preferredNS string) error {
	if d == nil {
		return fmt.Errorf("dynamic resource cache not initialized")
	}

	// Check if resource supports list/watch before attempting to watch
	if d.config.Discovery != nil && !d.config.Discovery.SupportsWatchGVR(gvr) {
		return fmt.Errorf("resource %s.%s/%s does not support list/watch", gvr.Resource, gvr.Group, gvr.Version)
	}

	if d.hasCoveringInformer(gvr, preferredNS) {
		return nil
	}

	// If CRD discovery is in progress, wait for it to finish
	if d.GetDiscoveryStatus() == CRDDiscoveryInProgress {
		select {
		case <-d.discoveryDone:
		case <-time.After(45 * time.Second):
			log.Printf("[dynamic cache] Timeout waiting for CRD discovery, probing %s independently", gvr.Resource)
		}
		if d.hasCoveringInformer(gvr, preferredNS) {
			return nil
		}
	}

	// Probe access (cluster-wide first, then fallback namespaces) BEFORE
	// acquiring the write lock; the result tells us which scopes to watch.
	// Singleflight per (gvr, preferredNS): concurrent callers — e.g. a
	// parallel enrichment fan-out over many objects of one unwatched kind —
	// share one probe fan-out + informer start instead of stampeding the
	// apiserver with redundant limit=1 lists.
	_, err, _ := d.watchStarts.Do(gvr.String()+"|"+preferredNS, func() (any, error) {
		if d.hasCoveringInformer(gvr, preferredNS) {
			return nil, nil
		}
		scopes, complete, err := d.probeScopes(gvr, preferredNS)
		if err != nil {
			return nil, fmt.Errorf("no access to %s.%s/%s: %w", gvr.Resource, gvr.Group, gvr.Version, err)
		}
		for _, scopeNS := range scopes {
			if err := d.startWatching(gvr, scopeNS); err != nil {
				return nil, err
			}
		}
		if preferredNS == "" && complete {
			// The all-namespaces scope for this GVR is settled — informers exist
			// for every granted scope. Mark it so the next all-namespaces read
			// doesn't re-probe cluster-wide plus every candidate. An incomplete
			// walk (deadline hit mid-fanout) is deliberately NOT marked, so the
			// next read finishes the job.
			d.mu.Lock()
			d.fallbackResolved[gvr] = true
			d.mu.Unlock()
		}
		return nil, nil
	})
	return err
}

// hasCoveringInformer reports whether an existing informer already serves
// reads for (gvr, ns), and must agree with readEntries: a cluster-wide
// informer covers every namespace; a specific ns is also covered by its own
// namespace-scoped informer. ns == "" is covered ONLY by a cluster-wide
// informer — not by incidental namespace-scoped ones, since readEntries(gvr,
// "") won't read those. Treating them as covering here would let
// ensureWatching skip the probe and then have List(gvr, "") find nothing,
// returning a spurious "informer not found" instead of probing cluster-wide
// (or returning a clean forbidden).
func (d *DynamicResourceCache) hasCoveringInformer(gvr schema.GroupVersionResource, ns string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if _, ok := d.informers[informerKey{gvr: gvr}]; ok {
		return true
	}
	if ns == "" {
		// Covered without a cluster-wide informer only when a previous
		// all-namespaces ensureWatching already fanned out across the
		// fallback namespaces; readEntries(gvr, "") unions those.
		return d.fallbackResolved[gvr]
	}
	_, ok := d.informers[informerKey{gvr: gvr, ns: ns}]
	return ok
}

// startWatching creates and starts an informer for (gvr, scopeNS), where
// scopeNS == "" is cluster-wide. No access probe — the caller has decided the
// scope. Each informer runs under its own context derived from the global
// stop channel, so a single informer can be cancelled independently (the
// idle reaper relies on this) while Stop() still tears them all down.
func (d *DynamicResourceCache) startWatching(gvr schema.GroupVersionResource, scopeNS string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.stopped {
		// A warmup or read probe can race Stop (context switch); informers
		// created now would never be shut down with the rest of the cache.
		return fmt.Errorf("dynamic cache stopped")
	}

	key := informerKey{gvr: gvr, ns: scopeNS}
	if _, exists := d.informers[key]; exists {
		return nil
	}
	// Enforce the "never both" invariant: a GVR has either one cluster-wide
	// informer OR namespace-scoped ones, never both (overlap would duplicate
	// objects in ListWatched and double-fire change callbacks).
	if scopeNS != "" {
		// A cluster-wide informer already covers this namespace — don't add a
		// redundant namespaced watch.
		if _, exists := d.informers[informerKey{gvr: gvr}]; exists {
			return nil
		}
	} else {
		// Starting cluster-wide supersedes any namespace-scoped informers for
		// this GVR; stop and drop them.
		for k, e := range d.informers {
			if k.gvr == gvr && k.ns != "" {
				e.cancel()
				delete(d.informers, k)
			}
		}
	}

	factory := d.factoryForNs(scopeNS)
	informer := factory.ForResource(gvr).Informer()
	// Apply the dynamic-cache transform BEFORE informer.Run so every
	// object entering the store is shrunk in place. SetTransform must
	// be called pre-Run (returns ErrRunning otherwise). If it ever
	// fails we log and continue — the informer still functions, just
	// with fattier cached objects.
	if err := informer.SetTransform(DropUnstructuredManagedFields); err != nil {
		log.Printf("Warning: SetTransform failed for %v: %v (cache will retain managedFields/CRD schemas)", gvr, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-d.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	d.informers[key] = &informerEntry{informer: informer, cancel: cancel}

	kind := d.gvrToKind(gvr)
	d.addDynamicChangeHandlers(informer, kind, gvr)
	// Re-apply handlers registered via AddGVRChangeHandler so informers created
	// lazily (or re-created after an idle reap) still feed derived caches.
	for _, h := range d.gvrHandlers[gvr] {
		if _, err := informer.AddEventHandler(h); err != nil {
			log.Printf("Warning: re-applying change handler for %v failed: %v", gvr, err)
		}
	}

	go informer.Run(ctx.Done())

	informerCount := len(d.informers)
	scopeDesc := "cluster-wide"
	if scopeNS != "" {
		scopeDesc = "namespace " + scopeNS
	}
	log.Printf("Started watching dynamic resource: %s.%s/%s (%s) (total dynamic informers: %d)", gvr.Resource, gvr.Group, gvr.Version, scopeDesc, informerCount)

	go func() {
		syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
		defer syncCancel()

		if !cache.WaitForCacheSync(syncCtx.Done(), informer.HasSynced) {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("Warning: cache sync timeout for %v (%s)", gvr, scopeDesc)
			}
		} else {
			log.Printf("Dynamic resource synced: %s.%s/%s (%s)", gvr.Resource, gvr.Group, gvr.Version, scopeDesc)
		}

		d.mu.Lock()
		if e := d.informers[key]; e != nil {
			e.synced = true
		}
		d.mu.Unlock()
	}()
	return nil
}

// nsCtxExpired reports whether a probe error is a context deadline/cancel —
// the one non-auth error class that must NOT fail open into a grant during
// the fanout walk.
func nsCtxExpired(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// fallbackNamespaces returns the configured namespace-fallback candidates,
// preferring the multi-namespace form.
// fanoutSettled reports whether the all-namespaces scope for gvr is fully
// resolved. A multi-candidate walk settles via the fallbackResolved marker;
// a SINGLE-candidate configuration is trivially settled the moment its one
// informer exists — a legacy --namespace user reading through explicit
// namespace requests must not have counts withheld waiting for an
// all-namespaces read that their UI never issues. Caller must hold d.mu.
func (d *DynamicResourceCache) fanoutSettledLocked(gvr schema.GroupVersionResource) bool {
	if d.fallbackResolved[gvr] {
		return true
	}
	fallbacks := d.fallbackNamespaces()
	if len(fallbacks) != 1 {
		return false
	}
	_, ok := d.informers[informerKey{gvr: gvr, ns: fallbacks[0]}]
	return ok
}

func (d *DynamicResourceCache) fallbackNamespaces() []string {
	if len(d.config.NamespaceFallbacks) > 0 {
		return d.config.NamespaceFallbacks
	}
	if d.config.NamespaceFallback != "" {
		return []string{d.config.NamespaceFallback}
	}
	return nil
}

// probeScopes decides which scopes to watch for gvr via limit=1 list probes.
// It returns the scope namespaces to start informers for — [""] means one
// cluster-wide informer — or a forbidden error when the identity can list
// the resource neither cluster-wide nor in any candidate namespace.
// preferredNS is the namespace the caller actually wants; when cluster-wide
// is denied and the caller named none, every configured fallback namespace
// is probed and each granted one becomes a scope.
func (d *DynamicResourceCache) probeScopes(gvr schema.GroupVersionResource, preferredNS string) (scopes []string, complete bool, err error) {
	// The budget covers the cluster-wide probe plus the whole candidate walk;
	// scale it with the candidate count so a 20-namespace fanout isn't judged
	// by a budget sized for the single-fallback case, while staying bounded
	// for the synchronous read paths that call ensureWatching.
	budget := 5 * time.Second
	if n := len(d.fallbackNamespaces()); n > 1 {
		budget = min(5*time.Second+time.Duration(n)*500*time.Millisecond, 15*time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	// Forced namespace mode (--namespace-scope): pin NAMESPACED resources to the
	// configured namespace. Cluster-scoped resources have no namespace dimension,
	// so they fall through to the cluster-wide path below — pinning them to a
	// namespace would list nothing.
	if d.config.NamespaceScoped && d.config.Namespace != "" && d.gvrIsNamespaced(gvr) {
		ns, err := d.classifyScope(gvr, d.config.Namespace, d.listProbe(ctx, gvr, d.config.Namespace))
		if err != nil {
			return nil, true, err
		}
		return []string{ns}, true, nil
	}

	// Cluster-wide first — one informer then serves every namespace.
	err = d.listProbe(ctx, gvr, "")
	if err == nil {
		return []string{""}, true, nil
	}
	if !isAuthProbeError(err) {
		// Transient/NotFound on proxy-fronted clusters — fail open to a
		// cluster-wide informer rather than disabling the kind; real
		// problems surface when the informer lists.
		log.Printf("[dynamic cache] Cluster-wide probe for %s.%s/%s returned non-auth error (allowing): %v", gvr.Resource, gvr.Group, gvr.Version, err)
		return []string{""}, true, nil
	}
	if !d.gvrIsNamespaced(gvr) {
		return nil, true, err // cluster-scoped resource, no namespace to fall back to
	}

	if preferredNS != "" {
		ns, err := d.classifyScope(gvr, preferredNS, d.listProbe(ctx, gvr, preferredNS))
		if err != nil {
			return nil, true, err
		}
		return []string{ns}, true, nil
	}

	fallbacks := d.fallbackNamespaces()
	if len(fallbacks) == 0 {
		return nil, true, err
	}
	granted := make([]string, 0, len(fallbacks))
	complete = true
	for _, ns := range fallbacks {
		if ctx.Err() != nil {
			complete = false
			break
		}
		// Per-candidate sub-deadline: without it a single consistently slow
		// namespace eats the whole walk budget on every retry, and since
		// retries restart from the first candidate, later namespaces would
		// never be reached at all.
		nsCtx, nsCancel := context.WithTimeout(ctx, 2*time.Second)
		probeErr := d.listProbe(nsCtx, gvr, ns)
		nsCancel()
		scoped, nsErr := d.classifyScope(gvr, ns, probeErr)
		if ctx.Err() != nil {
			// The probe ran into the shared deadline — classifyScope's
			// fail-open would turn the deadline error into a grant, starting
			// informers in namespaces that were never verified. Treat the
			// walk as incomplete instead so it retries on the next read.
			complete = false
			break
		}
		if nsErr == nil {
			if probeErr != nil && nsCtxExpired(probeErr) {
				// The candidate's own sub-deadline fired; classifyScope's
				// fail-open would count that as a grant. Record the walk as
				// incomplete instead so a later attempt re-tries it.
				complete = false
				continue
			}
			granted = append(granted, scoped)
		}
	}
	if len(granted) == 0 {
		return nil, complete, err // original cluster-wide forbidden — no candidate granted
	}
	return granted, complete, nil
}

func (d *DynamicResourceCache) listProbe(ctx context.Context, gvr schema.GroupVersionResource, namespace string) error {
	if namespace != "" {
		_, err := d.config.DynamicClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: 1})
		return err
	}
	_, err := d.config.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
	return err
}

// classifyScope maps a namespace probe result to (scope, err): success → watch
// ns; auth error → forbidden (no scope); non-auth error → fail open and watch
// ns anyway, matching the cluster-wide fail-open path.
func (d *DynamicResourceCache) classifyScope(gvr schema.GroupVersionResource, ns string, err error) (string, error) {
	if err == nil {
		return ns, nil
	}
	if isAuthProbeError(err) {
		return "", err
	}
	log.Printf("[dynamic cache] Probe for %s.%s/%s in namespace %q returned non-auth error (allowing): %v", gvr.Resource, gvr.Group, gvr.Version, ns, err)
	return ns, nil
}

// isAuthProbeError classifies an error as an auth (403/401) failure as
// opposed to a transient or NotFound error. Uses the typed K8s helpers only —
// substring matching on "forbidden"/"unauthorized" misclassifies admission-
// webhook denials and optimistic-concurrency conflicts ("Operation cannot be
// fulfilled ... forbidden") on proxy-fronted clusters, permanently disabling
// CRDs for the session.
func isAuthProbeError(err error) bool {
	if err == nil {
		return false
	}
	return apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
}

func (d *DynamicResourceCache) gvrIsNamespaced(gvr schema.GroupVersionResource) bool {
	if d.config.Discovery == nil {
		return true
	}
	resources, err := d.config.Discovery.GetAPIResources()
	if err != nil {
		return true
	}
	for _, res := range resources {
		if res.Group == gvr.Group && res.Version == gvr.Version && res.Name == gvr.Resource {
			return res.Namespaced
		}
	}
	return true
}

// ProbeCount does a quick list with limit=1 and returns the approximate resource count.
// Returns -1 if access is denied, -2 if the probe failed for non-auth reasons (caller
// should defer), or the count (items + remainingItemCount) on success.
//
// Exported so callers outside this package (e.g. internal/k8s when deciding
// whether to eager-warm high-cardinality CRDs like PolicyReports) can gate
// informer creation on cluster size before paying the watch-layer cost.
func (d *DynamicResourceCache) ProbeCount(gvr schema.GroupVersionResource) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var list *unstructured.UnstructuredList
	var err error
	list, err = d.probeCountList(ctx, gvr)

	if err != nil {
		if isAuthProbeError(err) {
			return -1
		}
		log.Printf("[dynamic cache] probeCount for %s.%s/%s returned non-auth error (deferring): %v",
			gvr.Resource, gvr.Group, gvr.Version, err)
		return -2
	}

	count := len(list.Items)
	if remaining := list.GetRemainingItemCount(); remaining != nil {
		count += int(*remaining)
		return count
	}
	if list.GetContinue() != "" {
		return -2
	}
	return count
}

func (d *DynamicResourceCache) probeCountList(ctx context.Context, gvr schema.GroupVersionResource) (*unstructured.UnstructuredList, error) {
	// Match probeScope: under --namespace-scope only NAMESPACED resources are
	// pinned to the configured namespace. Counting a cluster-scoped resource
	// inside a namespace lists nothing (or errors), which would misgate the
	// size-based eager-warm decision, so it stays cluster-wide.
	if d.config.NamespaceScoped && d.config.Namespace != "" && d.gvrIsNamespaced(gvr) {
		return d.config.DynamicClient.Resource(gvr).Namespace(d.config.Namespace).List(ctx, metav1.ListOptions{Limit: 1})
	}

	list, err := d.config.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
	if err == nil {
		return list, nil
	}
	if isAuthProbeError(err) && d.gvrIsNamespaced(gvr) {
		// Size heuristic only — the first granted fallback namespace is a
		// good-enough sample for the eager-warm decision.
		for _, ns := range d.fallbackNamespaces() {
			if nsList, nsErr := d.config.DynamicClient.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{Limit: 1}); nsErr == nil {
				return nsList, nil
			}
		}
	}
	return list, err
}

// gvrToKind converts a GVR to a Kind name using resource discovery.
func (d *DynamicResourceCache) gvrToKind(gvr schema.GroupVersionResource) string {
	if d.config.Discovery != nil {
		if kind := d.config.Discovery.GetKindForGVR(gvr); kind != "" {
			return kind
		}
	}
	// Fallback: capitalize and singularize
	name := gvr.Resource
	if len(name) > 1 && name[len(name)-1] == 's' {
		name = name[:len(name)-1]
	}
	if len(name) > 0 {
		return strings.ToUpper(name[:1]) + name[1:]
	}
	return name
}

// ---------------------------------------------------------------------------
// Change handlers
// ---------------------------------------------------------------------------

// safeCallback invokes fn with panic recovery to protect informer goroutines.
func (d *DynamicResourceCache) safeCallback(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ERROR: k8score dynamic cache %s callback panicked: %v", name, r)
		}
	}()
	fn()
}

func (d *DynamicResourceCache) addDynamicChangeHandlers(inf cache.SharedIndexInformer, kind string, gvr schema.GroupVersionResource) {
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			d.enqueueDynamicChange(kind, gvr, obj, nil, OpAdd)
		},
		UpdateFunc: func(oldObj, newObj any) {
			d.enqueueDynamicChange(kind, gvr, newObj, oldObj, OpUpdate)
		},
		DeleteFunc: func(obj any) {
			d.enqueueDynamicChange(kind, gvr, obj, nil, OpDelete)
		},
	})
}

func (d *DynamicResourceCache) enqueueDynamicChange(kind string, gvr schema.GroupVersionResource, obj any, oldObj any, op string) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			u, ok = tombstone.Obj.(*unstructured.Unstructured)
			if !ok {
				return
			}
		} else {
			return
		}
	}

	namespace := u.GetNamespace()
	name := u.GetName()
	uid := string(u.GetUID())

	if d.config.OnReceived != nil {
		d.safeCallback("OnReceived", func() { d.config.OnReceived(kind) })
	}

	// During initial sync, still fire OnChange (for historical recording)
	// but skip the channel send (no SSE flood).
	isSyncAdd := false
	if op == OpAdd {
		d.mu.RLock()
		synced := d.gvrSyncedLocked(gvr, namespace)
		d.mu.RUnlock()

		if !synced {
			isSyncAdd = true
			if d.config.DebugEvents {
				log.Printf("[DEBUG] Dynamic initial sync add event: %s/%s/%s (recording historical only)", kind, namespace, name)
			}
		}
	}

	var diff *DiffInfo
	if op == OpUpdate && oldObj != nil && obj != nil && d.config.ComputeDiff != nil {
		d.safeCallback("ComputeDiff", func() { diff = d.config.ComputeDiff(kind, oldObj, obj) })
	}

	change := ResourceChange{
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
		UID:       uid,
		Operation: op,
		Diff:      diff,
	}

	// Always fire OnChange (even during sync adds — Radar uses this for timeline)
	if d.config.OnChange != nil {
		d.safeCallback("OnChange", func() { d.config.OnChange(change, obj, oldObj) })
	}

	// Skip channel send during initial sync
	if isSyncAdd {
		return
	}

	// Send to change channel
	if d.config.Changes != nil {
		select {
		case d.config.Changes <- change:
		default:
			if d.config.OnDrop != nil {
				d.safeCallback("OnDrop", func() { d.config.OnDrop(kind, namespace, name, "channel_full", op) })
			}
			if d.config.DebugEvents {
				log.Printf("[DEBUG] Dynamic change channel full, dropped: %s/%s/%s op=%s", kind, namespace, name, op)
			}
		}
	}

	if d.config.OnRecorded != nil {
		d.safeCallback("OnRecorded", func() { d.config.OnRecorded(kind) })
	}
}

// ---------------------------------------------------------------------------
// Read methods
// ---------------------------------------------------------------------------

// WatchedGVRs returns the GVRs that already have a started informer. Callers
// that want to enumerate "what dynamic resources is the cache currently
// observing?" should use this — never iterate every CRD from API discovery
// and call List() on each, which spins up a new persistent informer per GVR
// and grows unbounded on clusters with many CRDs (Upbound AWS alone ships
// ~1000 kinds).
//
// Result is a snapshot; callers must not assume the set is stable across
// calls. Synced and unsynced informers are both included — callers that
// need only ready data should additionally call WaitForSync.
func (d *DynamicResourceCache) WatchedGVRs() []schema.GroupVersionResource {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.distinctGVRsLocked()
}

// distinctGVRsLocked returns the unique GVRs that have at least one informer,
// collapsing the per-namespace keys. Caller must hold d.mu.
func (d *DynamicResourceCache) distinctGVRsLocked() []schema.GroupVersionResource {
	seen := make(map[schema.GroupVersionResource]struct{}, len(d.informers))
	out := make([]schema.GroupVersionResource, 0, len(d.informers))
	for k := range d.informers {
		if _, ok := seen[k.gvr]; ok {
			continue
		}
		seen[k.gvr] = struct{}{}
		out = append(out, k.gvr)
	}
	return out
}

// readEntries returns the informer(s) that serve reads for (gvr, ns). A
// cluster-wide informer alone covers every namespace; otherwise reads come
// from namespace-scoped informers — the one matching a specific ns, or all of
// the matching namespace-scoped informer for a specific ns. e.informer is
// immutable after creation, so the returned entries are safe to read outside
// the lock (e.synced is not touched here). Returns nil when nothing covers
// (gvr, ns).
//
// ns == "" means cluster-wide and is served ONLY by a cluster-wide informer —
// it deliberately does NOT union whatever per-namespace informers happen to
// exist, which would make results depend on incidental cache state (and, in a
// shared cache, on namespaces another request warmed). Callers wanting a union
// over a known namespace set must name it via ListNamespaces.
func (d *DynamicResourceCache) readEntries(gvr schema.GroupVersionResource, ns string) []*informerEntry {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if e, ok := d.informers[informerKey{gvr: gvr}]; ok {
		return []*informerEntry{e}
	}
	if ns != "" {
		if e, ok := d.informers[informerKey{gvr: gvr, ns: ns}]; ok {
			return []*informerEntry{e}
		}
		return nil
	}
	// ns == "" ("all namespaces") with no cluster-wide informer. When Radar
	// connects with a namespace-restricted identity (fallbacks configured),
	// cluster-wide list was denied so the only informers for this GVR are
	// per-namespace — union them, otherwise an all-namespaces read returns
	// nothing even though the per-namespace informers are synced.
	//
	// Deliberately NOT gated on fallbackResolved: list surfaces are
	// best-effort and self-healing (every read re-runs ensureWatching until
	// the candidate walk settles), matching the typed probe's keep-partial
	// philosophy. Cardinality/absence assertions are the strict surfaces —
	// Count(gvr, nil), CountWatched, and IsClusterWideSynced all refuse an
	// unsettled fanout, because a partial number masquerades as a smaller
	// truth while a partial list is just an incomplete view. A cluster-wide
	// cache (no fallbacks configured) stays empty here, preserving the
	// deterministic cluster-wide-only contract that keeps incidental
	// per-namespace informers from leaking across users.
	if len(d.fallbackNamespaces()) == 0 {
		return nil
	}
	var out []*informerEntry
	for k, e := range d.informers {
		if k.gvr == gvr {
			out = append(out, e)
		}
	}
	return out
}

// indexerItems gathers raw store objects from the given entries, filtered to
// namespace when non-empty. Namespace-scoped informers hold disjoint
// namespaces, so unioning across entries cannot duplicate an object.
func indexerItems(entries []*informerEntry, namespace string) ([]any, error) {
	var items []any
	for _, e := range entries {
		idx := e.informer.GetIndexer()
		if namespace != "" {
			got, err := idx.ByIndex(cache.NamespaceIndex, namespace)
			if err != nil {
				return nil, err
			}
			items = append(items, got...)
		} else {
			items = append(items, idx.List()...)
		}
	}
	return items, nil
}

// gvrSyncedLocked reports whether the informer holding objects of gvr in
// namespace has finished its initial sync (cluster-wide informer first, else
// the namespace-scoped one). Caller must hold d.mu.
func (d *DynamicResourceCache) gvrSyncedLocked(gvr schema.GroupVersionResource, namespace string) bool {
	if e, ok := d.informers[informerKey{gvr: gvr}]; ok {
		return e.synced
	}
	if e, ok := d.informers[informerKey{gvr: gvr, ns: namespace}]; ok {
		return e.synced
	}
	return false
}

// getByKeyFromEntries returns the first store object matching key across the
// entries. Namespace-scoped informers hold disjoint namespaces, so at most one
// can hold a given namespaced key.
func getByKeyFromEntries(entries []*informerEntry, key string) (any, bool, error) {
	for _, e := range entries {
		item, ok, err := e.informer.GetIndexer().GetByKey(key)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return item, true, nil
		}
	}
	return nil, false, nil
}

// entriesSynced reports whether every entry's informer has completed its
// initial sync (informer.HasSynced is the thread-safe source of truth).
func entriesSynced(entries []*informerEntry) bool {
	for _, e := range entries {
		if !e.informer.HasSynced() {
			return false
		}
	}
	return len(entries) > 0
}

// Count returns the number of resources for a given GVR, optionally filtered by namespaces.
// Unlike List(), this avoids allocating a result slice and skips StripUnstructuredFields.
// Count reads only what is already watched and synced; it does not start informers.
//
// A cluster-wide informer serves any namespace filter. Without one, Count can
// answer for explicitly-named namespaces when each has its own synced informer.
// In namespace-fallback mode, Count(nil) unions the namespace-scoped informers
// so it agrees with all-namespace reads.
func (d *DynamicResourceCache) Count(gvr schema.GroupVersionResource, namespaces []string) (int, error) {
	if d == nil {
		return 0, fmt.Errorf("dynamic resource cache not initialized")
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	if e, ok := d.informers[informerKey{gvr: gvr}]; ok {
		if !e.informer.HasSynced() {
			return 0, fmt.Errorf("informer not found or not synced for %v", gvr)
		}
		if len(namespaces) == 0 {
			return len(e.informer.GetIndexer().List()), nil
		}
		return countByNamespaces(e, namespaces)
	}

	if len(namespaces) == 0 {
		// No cluster-wide informer. In namespace-fallback mode union the
		// per-namespace informers so Count agrees with
		// readEntries on an all-namespaces read; a cluster-wide cache keeps the
		// strict cluster-wide-only contract and errors.
		if len(d.fallbackNamespaces()) == 0 {
			return 0, fmt.Errorf("informer not found or not synced for %v", gvr)
		}
		// A truncated candidate walk leaves a partial namespace set whose sum
		// would silently under-count; only a settled fanout is authoritative.
		if !d.fanoutSettledLocked(gvr) {
			return 0, fmt.Errorf("informer not found or not synced for %v", gvr)
		}
		total := 0
		found := false
		for k, e := range d.informers {
			if k.gvr != gvr {
				continue
			}
			if !e.informer.HasSynced() {
				return 0, fmt.Errorf("informer not found or not synced for %v", gvr)
			}
			found = true
			total += len(e.informer.GetIndexer().List())
		}
		if !found {
			return 0, fmt.Errorf("informer not found or not synced for %v", gvr)
		}
		return total, nil
	}

	total := 0
	for _, ns := range namespaces {
		e, ok := d.informers[informerKey{gvr: gvr, ns: ns}]
		if !ok || !e.informer.HasSynced() {
			return 0, fmt.Errorf("informer not found or not synced for %v in namespace %s", gvr, ns)
		}
		n, err := countByNamespaces(e, []string{ns})
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// CountWatched returns cached object counts for every currently watched and
// synced GVR. It never starts informers and never probes the apiserver.
func (d *DynamicResourceCache) CountWatched(namespaces []string) map[schema.GroupVersionResource]int {
	if d == nil {
		return nil
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	type watchedSet struct {
		clusterWide *informerEntry
		byNamespace map[string]*informerEntry
	}
	byGVR := make(map[schema.GroupVersionResource]*watchedSet)
	for k, e := range d.informers {
		if !e.informer.HasSynced() {
			continue
		}
		set := byGVR[k.gvr]
		if set == nil {
			set = &watchedSet{byNamespace: make(map[string]*informerEntry)}
			byGVR[k.gvr] = set
		}
		if k.ns == "" {
			set.clusterWide = e
		} else {
			set.byNamespace[k.ns] = e
		}
	}

	// Unsynced entries were dropped above, so re-scan for GVRs whose fanout
	// still has an unsynced sibling — summing the synced subset would report
	// a silently smaller count instead of "not ready yet".
	partiallySynced := make(map[schema.GroupVersionResource]bool)
	for k, e := range d.informers {
		if !e.informer.HasSynced() {
			partiallySynced[k.gvr] = true
		}
	}

	counts := make(map[schema.GroupVersionResource]int)
	for gvr, set := range byGVR {
		if len(namespaces) == 0 {
			if set.clusterWide != nil {
				counts[gvr] = len(set.clusterWide.informer.GetIndexer().List())
				continue
			}
			if len(d.fallbackNamespaces()) == 0 {
				continue
			}
			// All-namespaces sums over a fanout are only authoritative once
			// the candidate walk settled and every informer synced.
			if !d.fanoutSettledLocked(gvr) || partiallySynced[gvr] {
				continue
			}
			total := 0
			for _, e := range set.byNamespace {
				total += len(e.informer.GetIndexer().List())
			}
			counts[gvr] = total
			continue
		}
		if set.clusterWide != nil {
			n, err := countByNamespaces(set.clusterWide, namespaces)
			if err == nil {
				counts[gvr] = n
			}
			continue
		}
		total := 0
		complete := true
		for _, ns := range namespaces {
			e, ok := set.byNamespace[ns]
			if !ok {
				complete = false
				break
			}
			n, err := countByNamespaces(e, []string{ns})
			if err != nil {
				complete = false
				break
			}
			total += n
		}
		if complete {
			counts[gvr] = total
		}
	}
	return counts
}

func countByNamespaces(e *informerEntry, namespaces []string) (int, error) {
	total := 0
	for _, ns := range namespaces {
		items, err := e.informer.GetIndexer().ByIndex(cache.NamespaceIndex, ns)
		if err != nil {
			return 0, fmt.Errorf("failed to count resources in namespace %s: %w", ns, err)
		}
		total += len(items)
	}
	return total, nil
}

func (d *DynamicResourceCache) CountDirectProbe(ctx context.Context, gvr schema.GroupVersionResource, namespaces []string, maxNamespaces, concurrency int) (int, error) {
	if d == nil {
		return 0, fmt.Errorf("dynamic resource cache not initialized")
	}
	if d.config.DynamicClient == nil {
		return 0, fmt.Errorf("dynamic client not initialized")
	}
	if len(namespaces) == 0 {
		return d.countDirectProbeOne(ctx, gvr, "")
	}
	if maxNamespaces > 0 && len(namespaces) > maxNamespaces {
		return 0, ErrResourceCountUnavailable
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(namespaces) {
		concurrency = len(namespaces)
	}

	type result struct {
		count int
		err   error
	}
	jobs := make(chan string)
	results := make(chan result, len(namespaces))
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ns := range jobs {
				n, err := d.countDirectProbeOne(ctx, gvr, ns)
				results <- result{count: n, err: err}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, ns := range namespaces {
			select {
			case <-ctx.Done():
				return
			case jobs <- ns:
			}
		}
	}()
	wg.Wait()
	close(results)

	total := 0
	for r := range results {
		if r.err != nil {
			return 0, r.err
		}
		total += r.count
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return total, nil
}

func (d *DynamicResourceCache) countDirectProbeOne(ctx context.Context, gvr schema.GroupVersionResource, namespace string) (int, error) {
	var list *unstructured.UnstructuredList
	var err error
	opts := metav1.ListOptions{Limit: directCountProbeLimit}
	if namespace != "" {
		list, err = d.config.DynamicClient.Resource(gvr).Namespace(namespace).List(ctx, opts)
	} else {
		list, err = d.config.DynamicClient.Resource(gvr).List(ctx, opts)
	}
	if err != nil {
		if isAuthProbeError(err) {
			return 0, ErrResourceCountUnavailable
		}
		return 0, fmt.Errorf("failed to probe resource count: %w", err)
	}
	if list == nil {
		return 0, ErrResourceCountUnavailable
	}
	if remaining := list.GetRemainingItemCount(); remaining != nil {
		return len(list.Items) + int(*remaining), nil
	}
	if list.GetContinue() == "" {
		return len(list.Items), nil
	}
	return 0, ErrResourceCountUnavailable
}

// List returns all resources of a given GVR, optionally filtered by namespace.
// This is non-blocking — returns whatever data is available immediately.
func (d *DynamicResourceCache) List(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	if d == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}

	if err := d.ensureWatching(gvr, namespace); err != nil {
		return nil, err
	}

	entries := d.readEntries(gvr, namespace)
	if len(entries) == 0 {
		return nil, fmt.Errorf("informer not found for %v", gvr)
	}

	items, err := indexerItems(entries, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}

	result := make([]*unstructured.Unstructured, 0, len(items))
	for _, item := range items {
		if u, ok := item.(*unstructured.Unstructured); ok {
			result = append(result, StripUnstructuredFields(u))
		}
	}

	return result, nil
}

// ListWatched returns every cached object for gvr across all currently-watched
// scopes (cluster-wide and/or per-namespace), unioned. It does NOT start or
// re-probe informers — it reads whatever is already watched. This is for
// internal "scan what's already cached" callers (Crossplane audit, Kyverno
// PolicyReport indexing) that iterate WatchedGVRs(): unlike List(gvr, ""),
// which is cluster-wide-only, it surfaces namespace-scoped contents so those
// scanners don't silently drop them in a namespace-restricted install.
// Request-facing reads must stay on List / ListNamespaces with explicit
// namespaces.
func (d *DynamicResourceCache) ListWatched(gvr schema.GroupVersionResource) ([]*unstructured.Unstructured, error) {
	if d == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}
	entries := d.entriesForGVR(gvr)
	items, err := indexerItems(entries, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}
	result := make([]*unstructured.Unstructured, 0, len(items))
	for _, item := range items {
		if u, ok := item.(*unstructured.Unstructured); ok {
			result = append(result, StripUnstructuredFields(u))
		}
	}
	return result, nil
}

// ListNamespaces returns resources of gvr unioned across an explicit set of
// namespaces. This is the sanctioned multi-namespace path — callers with a
// known, RBAC-filtered namespace set use it instead of List(gvr, ""), so the
// union is driven by the caller's explicit list rather than by whatever
// per-namespace informers incidentally exist. Namespace-scoped informers hold
// disjoint namespaces, so the union cannot duplicate an object.
//
// Two cases short-circuit to a cluster-wide read: an empty/nil set ("all"),
// and a cluster-scoped resource (no namespace dimension — a per-namespace
// filter would match nothing, so always read it cluster-wide regardless of the
// requested namespaces). The cluster-scoped check makes this safe to call
// uniformly over a mix of namespaced and cluster-scoped GVRs.
func (d *DynamicResourceCache) ListNamespaces(gvr schema.GroupVersionResource, namespaces []string) ([]*unstructured.Unstructured, error) {
	if d == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}
	if len(namespaces) == 0 || !d.gvrIsNamespaced(gvr) {
		return d.List(gvr, "")
	}
	result := make([]*unstructured.Unstructured, 0)
	for _, ns := range namespaces {
		items, err := d.List(gvr, ns)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	return result, nil
}

// ListBlocking returns all resources, waiting for cache sync first.
func (d *DynamicResourceCache) ListBlocking(gvr schema.GroupVersionResource, namespace string, timeout time.Duration) ([]*unstructured.Unstructured, error) {
	if d == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}

	if err := d.ensureWatching(gvr, namespace); err != nil {
		return nil, err
	}

	entries := d.readEntries(gvr, namespace)
	if len(entries) == 0 {
		return nil, fmt.Errorf("informer not found for %v", gvr)
	}

	for _, e := range entries {
		if !e.informer.HasSynced() {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			cache.WaitForCacheSync(ctx.Done(), e.informer.HasSynced)
			cancel()
		}
	}

	items, err := indexerItems(entries, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}

	result := make([]*unstructured.Unstructured, 0, len(items))
	for _, item := range items {
		if u, ok := item.(*unstructured.Unstructured); ok {
			result = append(result, StripUnstructuredFields(u))
		}
	}

	return result, nil
}

// Get returns a single resource by namespace and name.
func (d *DynamicResourceCache) Get(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	return d.get(gvr, namespace, name, false)
}

// GetPreserveLastApplied returns a single cached resource while preserving the
// kubectl last-applied annotation for internal drift computation.
func (d *DynamicResourceCache) GetPreserveLastApplied(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	return d.get(gvr, namespace, name, true)
}

func (d *DynamicResourceCache) get(gvr schema.GroupVersionResource, namespace, name string, preserveLastApplied bool) (*unstructured.Unstructured, error) {
	if d == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}

	if err := d.ensureWatching(gvr, namespace); err != nil {
		return nil, err
	}

	entries := d.readEntries(gvr, namespace)
	if len(entries) == 0 {
		return nil, fmt.Errorf("informer not found for %v", gvr)
	}

	var key string
	if namespace != "" {
		key = namespace + "/" + name
	} else {
		key = name
	}

	item, found, err := getByKeyFromEntries(entries, key)
	if err != nil {
		return nil, fmt.Errorf("failed to get resource: %w", err)
	}

	if !found && !entriesSynced(entries) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		for _, e := range entries {
			cache.WaitForCacheSync(ctx.Done(), e.informer.HasSynced)
		}
		cancel()

		item, found, err = getByKeyFromEntries(entries, key)
		if err != nil {
			return nil, fmt.Errorf("failed to get resource: %w", err)
		}
	}

	if !found {
		return nil, fmt.Errorf("%w: %s", ErrResourceNotFound, key)
	}

	u, ok := item.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("unexpected type in cache")
	}

	if preserveLastApplied {
		return StripUnstructuredFieldsPreserveLastApplied(u), nil
	}
	return StripUnstructuredFields(u), nil
}

// ListWithSelector returns resources matching a label selector.
func (d *DynamicResourceCache) ListWithSelector(gvr schema.GroupVersionResource, namespace string, selector labels.Selector) ([]*unstructured.Unstructured, error) {
	items, err := d.List(gvr, namespace)
	if err != nil {
		return nil, err
	}

	if selector == nil || selector.Empty() {
		return items, nil
	}

	result := make([]*unstructured.Unstructured, 0)
	for _, item := range items {
		if selector.Matches(labels.Set(item.GetLabels())) {
			result = append(result, item)
		}
	}

	return result, nil
}

// ListDirect fetches resources directly from the API (bypasses cache).
func (d *DynamicResourceCache) ListDirect(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	var list *unstructured.UnstructuredList
	var err error

	if namespace != "" {
		list, err = d.config.DynamicClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = d.config.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}

	result := make([]*unstructured.Unstructured, len(list.Items))
	for i := range list.Items {
		result[i] = StripUnstructuredFields(&list.Items[i])
	}

	return result, nil
}

// GetDirect fetches a single resource directly from the API (bypasses cache).
func (d *DynamicResourceCache) GetDirect(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	return d.getDirect(ctx, gvr, namespace, name, false)
}

// GetDirectPreserveLastApplied fetches a single resource directly from the
// API while preserving kubectl.kubernetes.io/last-applied-configuration. Used
// exclusively by GitOps drift detection, which needs the annotation to diff
// declared vs live state. Bypasses the dynamic informer entirely on purpose:
// caching this code path would otherwise force-start an informer for the
// resource's GVR (often core kinds like apps/Deployment, /v1/Service that
// Argo's status.resources references) and retain last-applied across every
// object cluster-wide — a meaningful memory regression to power a per-page
// diagnostic. Direct GET pays one API round-trip per managed resource per
// insight build (memoized 5s upstream).
func (d *DynamicResourceCache) GetDirectPreserveLastApplied(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	return d.getDirect(ctx, gvr, namespace, name, true)
}

func (d *DynamicResourceCache) getDirect(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, preserveLastApplied bool) (*unstructured.Unstructured, error) {
	var u *unstructured.Unstructured
	var err error

	if namespace != "" {
		u, err = d.config.DynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	} else {
		u, err = d.config.DynamicClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	}

	if err != nil {
		return nil, err
	}

	if preserveLastApplied {
		return StripUnstructuredFieldsPreserveLastApplied(u), nil
	}
	return StripUnstructuredFields(u), nil
}

// ---------------------------------------------------------------------------
// Batch / warmup
// ---------------------------------------------------------------------------

// WarmupParallel starts watching multiple resources in parallel and waits for all to sync.
func (d *DynamicResourceCache) WarmupParallel(gvrs []schema.GroupVersionResource, timeout time.Duration) {
	if d == nil || len(gvrs) == 0 {
		return
	}

	const maxConcurrentProbes = 50
	type probeResult struct {
		gvr      schema.GroupVersionResource
		scopes   []string
		complete bool
		ok       bool
	}
	results := make(chan probeResult, len(gvrs))
	sem := make(chan struct{}, maxConcurrentProbes)
	for _, gvr := range gvrs {
		go func(g schema.GroupVersionResource) {
			sem <- struct{}{}
			scopes, complete, err := d.probeScopes(g, "")
			<-sem
			results <- probeResult{gvr: g, scopes: scopes, complete: complete, ok: err == nil}
		}(gvr)
	}

	var accessible []probeResult
	for range gvrs {
		r := <-results
		if r.ok {
			accessible = append(accessible, r)
		}
	}

	if len(accessible) == 0 {
		return
	}

	var started []informerKey
	for _, r := range accessible {
		allStarted := true
		for _, scope := range r.scopes {
			if err := d.startWatching(r.gvr, scope); err == nil {
				started = append(started, informerKey{gvr: r.gvr, ns: scope})
			} else {
				allStarted = false
			}
		}
		if allStarted && r.complete {
			d.mu.Lock()
			d.fallbackResolved[r.gvr] = true
			d.mu.Unlock()
		}
	}

	if len(started) == 0 {
		return
	}

	d.mu.RLock()
	syncFuncs := make([]cache.InformerSynced, 0, len(started))
	for _, key := range started {
		if e, ok := d.informers[key]; ok {
			syncFuncs = append(syncFuncs, e.informer.HasSynced)
		}
	}
	d.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if !cache.WaitForCacheSync(ctx.Done(), syncFuncs...) {
		log.Printf("Warning: not all dynamic caches synced within timeout")
	} else {
		log.Printf("All %d dynamic resources synced", len(syncFuncs))
	}
}

// DiscoverAllCRDs discovers all CRDs that support list/watch and decides which
// to watch eagerly vs on-demand. Known integrations (cert-manager, KEDA, etc.)
// are already watching from WarmupCommonCRDs. For the rest, CRDs with ≤100
// resources are watched eagerly (cheap, full timeline coverage). CRDs with >100
// resources (calico, cilium, etc.) are deferred to on-demand via EnsureWatching()
// when the user browses them, avoiding expensive watch connections.
func (d *DynamicResourceCache) DiscoverAllCRDs() {
	if d == nil {
		log.Println("[CRD Discovery] Cache is nil, skipping")
		return
	}

	d.discoveryMu.Lock()
	if d.discoveryStatus != CRDDiscoveryIdle {
		log.Printf("[CRD Discovery] Already in status: %s, skipping", d.discoveryStatus)
		d.discoveryMu.Unlock()
		return
	}
	d.discoveryStatus = CRDDiscoveryInProgress
	d.discoveryMu.Unlock()
	log.Println("[CRD Discovery] Starting CRD discovery...")

	go func() {
		defer func() {
			panicked := false
			if r := recover(); r != nil {
				panicked = true
				buf := make([]byte, 4096)
				n := runtime.Stack(buf, false)
				log.Printf("PANIC in CRD discovery goroutine: %v\n%s", r, buf[:n])
			}
			d.discoveryMu.Lock()
			if d.discoveryStatus != CRDDiscoveryComplete {
				d.discoveryStatus = CRDDiscoveryComplete
				close(d.discoveryDone)
			}
			d.discoveryMu.Unlock()
			if panicked {
				log.Println("[CRD Discovery] CRD discovery terminated due to panic (marked complete to unblock waiters)")
			} else {
				log.Println("[CRD Discovery] CRD discovery complete")
			}

			d.notifyCRDDiscoveryComplete()
		}()

		if d.config.Discovery == nil {
			log.Println("Resource discovery not available for CRD discovery")
			return
		}

		resources, err := d.config.Discovery.GetAPIResources()
		if err != nil {
			log.Printf("Failed to get API resources for CRD discovery: %v", err)
			return
		}

		best := make(map[string]schema.GroupVersionResource)
		for _, res := range resources {
			if !res.IsCRD {
				continue
			}
			// Crossplane serves this legacy API with a deprecation warning on every
			// informer watch renewal. Keep it available on demand, but don't eagerly watch it.
			if res.Group == "apiextensions.crossplane.io" && res.Name == "usages" {
				continue
			}
			hasList := false
			hasWatch := false
			for _, verb := range res.Verbs {
				if verb == "list" {
					hasList = true
				}
				if verb == "watch" {
					hasWatch = true
				}
			}
			if !hasList || !hasWatch {
				continue
			}
			key := res.Group + "/" + res.Name
			if existing, ok := best[key]; ok {
				if !IsMoreStableVersion(res.Version, existing.Version) {
					continue
				}
			}
			best[key] = schema.GroupVersionResource{
				Group:    res.Group,
				Version:  res.Version,
				Resource: res.Name,
			}
		}

		var gvrs []schema.GroupVersionResource
		for _, gvr := range best {
			gvrs = append(gvrs, gvr)
		}

		if len(gvrs) == 0 {
			log.Println("No watchable CRDs found")
			return
		}

		// Filter out GVRs already watched from Phase 1 warmup
		d.mu.RLock()
		watched := make(map[schema.GroupVersionResource]struct{}, len(d.informers))
		for k := range d.informers {
			watched[k.gvr] = struct{}{}
		}
		alreadyWatching := len(watched)
		var remaining []schema.GroupVersionResource
		for _, gvr := range gvrs {
			if _, exists := watched[gvr]; !exists {
				remaining = append(remaining, gvr)
			}
		}
		d.mu.RUnlock()

		if len(remaining) == 0 {
			log.Printf("Discovered %d watchable CRDs (all %d already watching from warmup)", len(gvrs), alreadyWatching)
			return
		}

		// Probe each remaining CRD to get resource count. CRDs with few resources
		// (≤100) are cheap to watch and give full timeline coverage. CRDs with many
		// resources (calico policies, cilium endpoints) are deferred to on-demand.
		const maxEagerResources = 100
		const maxConcurrentProbes = 50
		type probeResult struct {
			gvr   schema.GroupVersionResource
			count int // -1 = no access
		}
		results := make(chan probeResult, len(remaining))
		sem := make(chan struct{}, maxConcurrentProbes)
		for _, gvr := range remaining {
			go func(g schema.GroupVersionResource) {
				sem <- struct{}{}
				defer func() {
					<-sem
					if r := recover(); r != nil {
						log.Printf("[CRD Discovery] Panic probing %s.%s/%s: %v", g.Resource, g.Group, g.Version, r)
						results <- probeResult{gvr: g, count: -1}
					}
				}()
				count := d.ProbeCount(g)
				results <- probeResult{gvr: g, count: count}
			}(gvr)
		}

		var eager []schema.GroupVersionResource
		var deferredCount int
		var noAccessCount int
		for range remaining {
			r := <-results
			if r.count == -1 {
				noAccessCount++
				continue
			}
			if r.count == -2 {
				// Probe failed (timeout, network error) — defer to be safe
				deferredCount++
				continue
			}
			if r.count <= maxEagerResources {
				eager = append(eager, r.gvr)
			} else {
				deferredCount++
				if d.config.DebugEvents {
					kind := d.gvrToKind(r.gvr)
					log.Printf("[CRD Discovery] Deferring %s (%d resources > %d threshold)", kind, r.count, maxEagerResources)
				}
			}
		}

		log.Printf("Discovered %d watchable CRDs (%d already watching, %d small → eager, %d large → on-demand, %d no access)",
			len(gvrs), alreadyWatching, len(eager), deferredCount, noAccessCount)

		if len(eager) > 0 {
			d.WarmupParallel(eager, 30*time.Second)
		}
	}()
}

// ---------------------------------------------------------------------------
// Discovery status / sync
// ---------------------------------------------------------------------------

// GetDiscoveryStatus returns the current CRD discovery status.
func (d *DynamicResourceCache) GetDiscoveryStatus() CRDDiscoveryStatus {
	if d == nil {
		return CRDDiscoveryIdle
	}

	d.discoveryMu.RLock()
	defer d.discoveryMu.RUnlock()

	return d.discoveryStatus
}

// entriesForGVR returns every informer entry watching gvr across all scopes —
// the cluster-wide entry and/or per-namespace entries. GVR-level status and
// handler-registration APIs ask "is this kind watched?" rather than returning
// resource data, so they span all of a GVR's namespace-scoped informers —
// unlike readEntries, whose ns == "" path is cluster-wide-only to keep data
// reads deterministic. Using readEntries(gvr, "") here would wrongly report a
// namespace-restricted (no cluster-wide informer) GVR as unwatched/unsynced.
func (d *DynamicResourceCache) entriesForGVR(gvr schema.GroupVersionResource) []*informerEntry {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var out []*informerEntry
	for k, e := range d.informers {
		if k.gvr == gvr {
			out = append(out, e)
		}
	}
	return out
}

// WaitForSync waits for a resource's cache(s) to be synced (with timeout).
// A GVR may have several namespace-scoped informers; all must sync.
func (d *DynamicResourceCache) WaitForSync(gvr schema.GroupVersionResource, timeout time.Duration) bool {
	entries := d.entriesForGVR(gvr)
	if len(entries) == 0 {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	syncFuncs := make([]cache.InformerSynced, 0, len(entries))
	for _, e := range entries {
		syncFuncs = append(syncFuncs, e.informer.HasSynced)
	}
	return cache.WaitForCacheSync(ctx.Done(), syncFuncs...)
}

// IsSynced checks if a resource's cache(s) are synced (non-blocking). It
// reports on the informers that EXIST — an unsettled fallback fanout can be
// synced-but-partial. Callers asserting completeness or absence must use
// IsClusterWideSynced (or Count, which refuses unsettled fanouts) instead.
func (d *DynamicResourceCache) IsSynced(gvr schema.GroupVersionResource) bool {
	return entriesSynced(d.entriesForGVR(gvr))
}

// IsClusterWideSynced reports whether gvr is served by a single cluster-wide
// informer that has finished syncing. When true, ListWatched(gvr) is
// authoritative for EVERY namespace — the caller can safely conclude an object
// is absent cluster-wide. When false (namespace-scoped fallback, unsynced, or
// unwatched), the cache may only know a subset of namespaces, so "not found"
// must NOT be treated as "doesn't exist". The "never both" informer invariant
// (a GVR has either one cluster-wide informer or namespaced ones, never both)
// makes the cluster-wide check authoritative.
func (d *DynamicResourceCache) IsClusterWideSynced(gvr schema.GroupVersionResource) bool {
	if d == nil {
		return false
	}
	// Explicitly require the cluster-wide informer — hasCoveringInformer
	// also reports true for a resolved namespace-fallback fanout, whose
	// coverage is NOT cluster-wide (callers use this to assert absence).
	d.mu.RLock()
	_, clusterWide := d.informers[informerKey{gvr: gvr}]
	d.mu.RUnlock()
	return clusterWide && d.IsSynced(gvr)
}

// ---------------------------------------------------------------------------
// Introspection
// ---------------------------------------------------------------------------

// GetWatchedResources returns a list of GVRs currently being watched.
func (d *DynamicResourceCache) GetWatchedResources() []schema.GroupVersionResource {
	if d == nil {
		return nil
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.distinctGVRsLocked()
}

// GetInformerCount returns the number of active dynamic informers.
func (d *DynamicResourceCache) GetInformerCount() int {
	if d == nil {
		return 0
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	return len(d.informers)
}

// AddGVRChangeHandler registers a change handler on the informer for the
// given GVR. The handler fires for add/update/delete events (including the
// initial sync). Returns an error if no informer exists yet for the GVR —
// callers should warm up or EnsureWatching the resource before registering.
//
// Used by derived caches (PolicyReport index, etc.) that need to react to
// changes on a single resource kind without subscribing to the global
// OnChange callback (which would fire for every dynamic resource).
//
// The handler runs on the informer's event-processing goroutine; it must
// be non-blocking. A panic in the handler is contained by the upstream
// informer machinery (no impact on other handlers).
func (d *DynamicResourceCache) AddGVRChangeHandler(gvr schema.GroupVersionResource, handler cache.ResourceEventHandler) error {
	if d == nil {
		return fmt.Errorf("dynamic resource cache not initialized")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	var entries []*informerEntry
	for k, e := range d.informers {
		if k.gvr == gvr {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return fmt.Errorf("no informer for %s.%s/%s; warm up the resource before registering a handler", gvr.Resource, gvr.Group, gvr.Version)
	}

	// Remember the handler so informers started later for this GVR (lazy
	// per-namespace watches, or re-creations after an idle reap) get it too —
	// otherwise derived caches silently miss those namespaces' events.
	d.gvrHandlers[gvr] = append(d.gvrHandlers[gvr], handler)

	for _, e := range entries {
		if _, err := e.informer.AddEventHandler(handler); err != nil {
			return fmt.Errorf("add event handler for %v: %w", gvr, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// CRD discovery callbacks
// ---------------------------------------------------------------------------

// OnCRDDiscoveryComplete registers a callback to be called when CRD discovery completes.
func (d *DynamicResourceCache) OnCRDDiscoveryComplete(callback func()) {
	d.crdCallbacksMu.Lock()
	defer d.crdCallbacksMu.Unlock()
	d.crdCallbacks = append(d.crdCallbacks, callback)
}

func (d *DynamicResourceCache) notifyCRDDiscoveryComplete() {
	d.crdCallbacksMu.RLock()
	defer d.crdCallbacksMu.RUnlock()
	for _, cb := range d.crdCallbacks {
		go cb()
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Stop initiates a non-blocking shutdown of the dynamic cache.
func (d *DynamicResourceCache) Stop() {
	if d == nil {
		return
	}

	d.stopOnce.Do(func() {
		log.Println("Stopping dynamic resource cache")

		d.mu.Lock()
		d.stopped = true
		d.mu.Unlock()

		d.discoveryMu.Lock()
		if d.discoveryStatus != CRDDiscoveryComplete {
			d.discoveryStatus = CRDDiscoveryComplete
			close(d.discoveryDone)
		}
		d.discoveryMu.Unlock()

		// Closing stopCh cancels every per-informer context (each informer's
		// watchdog goroutine selects on it), stopping all watches.
		close(d.stopCh)

		d.mu.RLock()
		nsFactories := make([]dynamicinformer.DynamicSharedInformerFactory, 0, len(d.nsFactories))
		for _, f := range d.nsFactories {
			nsFactories = append(nsFactories, f)
		}
		d.mu.RUnlock()

		go func() {
			done := make(chan struct{})
			go func() {
				d.factory.Shutdown()
				for _, f := range nsFactories {
					f.Shutdown()
				}
				close(done)
			}()
			select {
			case <-done:
				log.Println("Dynamic resource cache factory shutdown complete")
			case <-time.After(5 * time.Second):
				log.Println("Dynamic resource cache factory shutdown taking >5s, abandoning (goroutine will finish on its own)")
			}
		}()
	})
}
