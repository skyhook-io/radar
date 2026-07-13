package k8score

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/pager"
)

// ResourceCache provides fast, eventually-consistent access to K8s resources
// using SharedInformers. It is the shared core used by both Radar and
// skyhook-connector.
type ResourceCache struct {
	factory          informers.SharedInformerFactory            // cluster-wide factory (always present)
	nsFactories      map[string]informers.SharedInformerFactory // per-namespace factories (for mixed-scope mode)
	factoryByKind    map[string]informers.SharedInformerFactory // resolved factory per enabled kind — listers MUST go through this
	indexerByKind    map[string]cache.Indexer                   // union indexer for kinds watched in multiple namespace factories
	pagedInformers   map[string]cache.SharedIndexInformer       // per-kind paginating informers (ListPageSize>0); listers read these instead of the factory's
	changes          chan ResourceChange
	stopCh           chan struct{}
	stopOnce         sync.Once
	enabledResources map[string]bool
	deferredSynced   map[string]bool
	deferredMu       sync.RWMutex
	deferredDone     chan struct{}
	deferredFailed   atomic.Bool // true if WaitForCacheSync failed for deferred informers
	syncComplete     atomic.Bool
	config           CacheConfig
	stdlog           *log.Logger

	// Per-informer sync tracking for diagnostics
	informerStatuses []InformerSyncStatus
	informerMu       sync.RWMutex
	promotedKinds    []string // set when SyncTimeout fires; empty on normal sync
	syncStartTime    time.Time
}

// InformerSyncStatus tracks the sync state of a single informer.
type InformerSyncStatus struct {
	Kind          string `json:"kind"`
	Key           string `json:"key"`      // e.g. "pods", "deployments"
	Deferred      bool   `json:"deferred"` // true if deferred (non-critical)
	Synced        bool   `json:"synced"`
	SyncedAt      string `json:"syncedAt,omitempty"`      // RFC3339 timestamp
	Items         int    `json:"items"`                   // current item count in lister
	LastError     string `json:"lastError,omitempty"`     // most recent watch error (truncated to keep diagnostics small)
	LastErrorAt   string `json:"lastErrorAt,omitempty"`   // RFC3339 timestamp of last watch error
	ForbiddenSeen bool   `json:"forbiddenSeen,omitempty"` // reflector hit 403/401 — probe and reality disagree
}

// SyncPhase describes the current phase of cache initialization.
type SyncPhase string

const (
	SyncPhaseNotStarted SyncPhase = "not_started"
	SyncPhaseCritical   SyncPhase = "syncing_critical"
	SyncPhaseDeferred   SyncPhase = "syncing_deferred"
	SyncPhaseComplete   SyncPhase = "complete"
)

// CacheSyncStatus is the overall sync status exposed for diagnostics.
type CacheSyncStatus struct {
	Phase           SyncPhase            `json:"phase"`
	SyncStarted     string               `json:"syncStarted,omitempty"` // RFC3339
	ElapsedSec      float64              `json:"elapsedSec"`
	CriticalTotal   int                  `json:"criticalTotal"`
	CriticalSynced  int                  `json:"criticalSynced"`
	DeferredTotal   int                  `json:"deferredTotal"`
	DeferredSynced  int                  `json:"deferredSynced"`
	Informers       []InformerSyncStatus `json:"informers"`
	PendingCritical []string             `json:"pendingCritical,omitempty"` // kinds not yet synced
	PendingDeferred []string             `json:"pendingDeferred,omitempty"`
	PromotedKinds   []string             `json:"promotedKinds,omitempty"` // critical informers that timed out
}

type informerSetup struct {
	key             string
	kind            string
	setup           func(factory informers.SharedInformerFactory) cache.SharedIndexInformer
	isEvent         bool
	isClusterScoped bool // true for nodes, namespaces, PV, storageclasses, ingressclasses
	// pagedSetup, when non-nil and CacheConfig.ListPageSize > 0, builds a
	// paginating informer for this kind instead of the factory one. Set only
	// for high-cardinality kinds whose single-shot initial LIST can fail on
	// very large clusters.
	pagedSetup func(client kubernetes.Interface, namespace string, pageSize int64) cache.SharedIndexInformer
}

// newPagedInformer builds a SharedIndexInformer whose initial LIST is fetched in
// pages via a consistent (resourceVersion="") read rather than one unpaginated
// response. The watch is unchanged. On clusters without WatchList streaming, a
// single giant LIST of a high-cardinality kind can exceed the response-read
// deadline or spike memory; paging bounds each request. When WatchList IS
// available, client-go streams the initial state and never calls this ListFunc.
func newPagedInformer(
	example apiruntime.Object,
	pageSize int64,
	listFn pager.ListPageFunc,
	watchFn func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error),
) cache.SharedIndexInformer {
	lw := &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, _ metav1.ListOptions) (apiruntime.Object, error) {
			p := pager.New(listFn)
			p.PageSize = pageSize
			// resourceVersion="" forces a consistent read the apiserver will
			// paginate — RV=0 (watch-cache) reads ignore limit. We deliberately
			// override the reflector's default RV here.
			obj, _, err := p.List(ctx, metav1.ListOptions{ResourceVersion: ""})
			return obj, err
		},
		WatchFuncWithContext: watchFn,
	}
	inf := cache.NewSharedIndexInformer(lw, example, 0, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	// Factory informers get DropManagedFields via WithTransform; these are built
	// outside the factory, so apply the same transform directly. SetTransform
	// only errors once started, which hasn't happened yet.
	_ = inf.SetTransform(DropManagedFields)
	return inf
}

func cloneScopeNamespaces(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		if len(v) == 0 {
			continue
		}
		out[k] = append([]string(nil), v...)
	}
	return out
}

func resourceScopeNamespaces(key string, scope ResourceScope, byKind map[string][]string) []string {
	if !scope.Enabled || scope.Namespace == "" {
		return nil
	}
	namespaces := byKind[key]
	if len(namespaces) == 0 {
		return []string{scope.Namespace}
	}
	seen := make(map[string]struct{}, len(namespaces)+1)
	out := make([]string, 0, len(namespaces)+1)
	add := func(ns string) {
		if ns == "" {
			return
		}
		if _, ok := seen[ns]; ok {
			return
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	add(scope.Namespace)
	for _, ns := range namespaces {
		add(ns)
	}
	return out
}

// unionIndexer is a read-only cache.Indexer that fans reads out across the
// per-namespace informer stores of one kind. Each underlying informer watches
// a distinct namespace, so the union cannot duplicate an object. Reading the
// informers' own stores — rather than mirroring events into a separate
// indexer — keeps HasSynced meaningful (an informer's store is fully
// populated before HasSynced flips, while a mirror fills in asynchronously
// after) and avoids holding every object twice.
type unionIndexer struct {
	indexers []cache.Indexer
}

var errUnionIndexerReadOnly = errors.New("union indexer is read-only")

func (u *unionIndexer) Add(any) error   { return errUnionIndexerReadOnly }
func (u *unionIndexer) Bookmark(string) {}

// LastStoreSyncResourceVersion is only meaningful on stores a reflector
// writes into; nothing writes into the union, so report "unknown".
func (u *unionIndexer) LastStoreSyncResourceVersion() string { return "" }
func (u *unionIndexer) Update(any) error                     { return errUnionIndexerReadOnly }
func (u *unionIndexer) Delete(any) error                     { return errUnionIndexerReadOnly }
func (u *unionIndexer) Replace([]any, string) error          { return errUnionIndexerReadOnly }
func (u *unionIndexer) Resync() error                        { return errUnionIndexerReadOnly }
func (u *unionIndexer) AddIndexers(cache.Indexers) error     { return errUnionIndexerReadOnly }

func (u *unionIndexer) List() []any {
	var out []any
	for _, idx := range u.indexers {
		out = append(out, idx.List()...)
	}
	return out
}

func (u *unionIndexer) ListKeys() []string {
	var out []string
	for _, idx := range u.indexers {
		out = append(out, idx.ListKeys()...)
	}
	return out
}

func (u *unionIndexer) Get(obj any) (any, bool, error) {
	for _, idx := range u.indexers {
		if item, exists, err := idx.Get(obj); err != nil || exists {
			return item, exists, err
		}
	}
	return nil, false, nil
}

func (u *unionIndexer) GetByKey(key string) (any, bool, error) {
	for _, idx := range u.indexers {
		if item, exists, err := idx.GetByKey(key); err != nil || exists {
			return item, exists, err
		}
	}
	return nil, false, nil
}

func (u *unionIndexer) Index(indexName string, obj any) ([]any, error) {
	var out []any
	for _, idx := range u.indexers {
		items, err := idx.Index(indexName, obj)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

func (u *unionIndexer) IndexKeys(indexName, indexedValue string) ([]string, error) {
	var out []string
	for _, idx := range u.indexers {
		keys, err := idx.IndexKeys(indexName, indexedValue)
		if err != nil {
			return nil, err
		}
		out = append(out, keys...)
	}
	return out, nil
}

func (u *unionIndexer) ListIndexFuncValues(indexName string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, idx := range u.indexers {
		for _, v := range idx.ListIndexFuncValues(indexName) {
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

func (u *unionIndexer) ByIndex(indexName, indexedValue string) ([]any, error) {
	var out []any
	for _, idx := range u.indexers {
		items, err := idx.ByIndex(indexName, indexedValue)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

func (u *unionIndexer) GetIndexers() cache.Indexers {
	if len(u.indexers) == 0 {
		return cache.Indexers{}
	}
	return u.indexers[0].GetIndexers()
}

// NewResourceCache creates and starts a ResourceCache from the given config.
// Startup has three tiers:
//   - Critical informers block until synced. With PatienceWindow + MinimalSet,
//     the cache returns as soon as the minimal set is ready *after* the
//     patience window elapses (rest of critical promoted to deferred). With
//     SyncTimeout, the cache returns at most after the timeout.
//   - Deferred informers sync in the background after critical completes.
//   - Background informers (e.g. Events) sync independently on their own goroutine.
func NewResourceCache(cfg CacheConfig) (*ResourceCache, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("CacheConfig.Client must not be nil")
	}
	if cfg.NamespaceScoped && cfg.Namespace == "" {
		return nil, fmt.Errorf("CacheConfig.Namespace must be set when NamespaceScoped is true")
	}
	for k, scope := range cfg.ResourceScopes {
		if scope.Enabled && scope.Namespace == "" {
			continue
		}
		if scope.Enabled && scope.Namespace != "" && k == "" {
			return nil, fmt.Errorf("CacheConfig.ResourceScopes contains entry with empty key")
		}
	}
	for k, namespaces := range cfg.ResourceScopeNamespaces {
		if k == "" {
			return nil, fmt.Errorf("CacheConfig.ResourceScopeNamespaces contains entry with empty key")
		}
		if len(namespaces) == 0 {
			continue
		}
		scope, ok := cfg.ResourceScopes[k]
		if !ok || !scope.Enabled {
			return nil, fmt.Errorf("CacheConfig.ResourceScopeNamespaces contains namespaces for disabled key %q", k)
		}
		if scope.Namespace == "" {
			return nil, fmt.Errorf("CacheConfig.ResourceScopeNamespaces contains namespaces for cluster-wide key %q", k)
		}
	}

	channelSize := cfg.ChannelSize
	if channelSize <= 0 {
		channelSize = 10000
	}

	logf := cfg.TimingLogger
	if logf == nil {
		logf = func(string, ...any) {} // no-op
	}

	stdlog := cfg.Logger
	if stdlog == nil {
		stdlog = log.Default()
	}

	// Clone caller-owned maps to prevent mutation after construction.
	cfg.ResourceTypes = maps.Clone(cfg.ResourceTypes)
	cfg.ResourceScopes = maps.Clone(cfg.ResourceScopes)
	cfg.ResourceScopeNamespaces = cloneScopeNamespaces(cfg.ResourceScopeNamespaces)
	cfg.DeferredTypes = maps.Clone(cfg.DeferredTypes)
	cfg.MinimalSet = maps.Clone(cfg.MinimalSet)

	// Derive a per-kind scope view that captures both modes:
	//   - ResourceScopes (when set) is authoritative.
	//   - Otherwise fall back to ResourceTypes + NamespaceScoped + Namespace.
	// This single map drives both the enabled set and which factory each
	// informer is wired into.
	scopes := make(map[string]ResourceScope)
	if cfg.ResourceScopes != nil {
		for k, s := range cfg.ResourceScopes {
			if s.Enabled {
				scopes[k] = s
			}
		}
	} else {
		ns := ""
		if cfg.NamespaceScoped {
			ns = cfg.Namespace
		}
		for k, ok := range cfg.ResourceTypes {
			if ok {
				scopes[k] = ResourceScope{Enabled: true, Namespace: ns}
			}
		}
	}

	stopCh := make(chan struct{})
	changes := make(chan ResourceChange, channelSize)

	// Build one factory per unique scope. The cluster-wide factory is
	// always created so cluster-scoped kinds (nodes, namespaces, PV…)
	// have a home; namespace-scoped factories are created on demand.
	clusterFactory := informers.NewSharedInformerFactoryWithOptions(
		cfg.Client,
		0, // no resync — updates come via watch
		informers.WithTransform(DropManagedFields),
	)
	nsFactories := make(map[string]informers.SharedInformerFactory)
	for key, s := range scopes {
		namespaces := resourceScopeNamespaces(key, s, cfg.ResourceScopeNamespaces)
		if len(namespaces) == 0 {
			continue
		}
		for _, namespace := range namespaces {
			if _, ok := nsFactories[namespace]; ok {
				continue
			}
			nsFactories[namespace] = informers.NewSharedInformerFactoryWithOptions(
				cfg.Client,
				0,
				informers.WithTransform(DropManagedFields),
				informers.WithNamespace(namespace),
			)
		}
	}
	if len(nsFactories) > 0 {
		var nsList []string
		for ns := range nsFactories {
			nsList = append(nsList, ns)
		}
		sort.Strings(nsList)
		// %q on the slice quotes each element, neutering newlines / control
		// characters in caller-supplied namespace strings before they hit logs.
		stdlog.Printf("Using namespace-scoped informers for namespace(s): %q", nsList)
	}

	// pickFactory routes each informer to the factory matching its scope.
	// Cluster-scoped kinds always use the cluster-wide factory regardless
	// of caller intent — namespace-scoping them would 404.
	pickFactory := func(s informerSetup) informers.SharedInformerFactory {
		scope, ok := scopes[s.key]
		if !ok || scope.Namespace == "" || s.isClusterScoped {
			return clusterFactory
		}
		return nsFactories[scope.Namespace]
	}

	// Table-driven informer setup — only create informers for enabled types
	setups := buildInformerSetups()

	// enabledMap is the boolean projection over `scopes` exposed via
	// GetEnabledResources for callers that only need "is this kind on?"
	// without the per-kind scope detail.
	enabled := make(map[string]bool, len(scopes))
	for k := range scopes {
		enabled[k] = true
	}
	deferredTypes := cfg.DeferredTypes
	if deferredTypes == nil {
		deferredTypes = map[string]bool{}
	}

	var criticalSyncFuncs []cache.InformerSynced
	var deferredSyncFuncs []cache.InformerSynced
	var deferredKeys []string
	var backgroundSyncFuncs []cache.InformerSynced // Events — sync independently, don't block deferredDone
	var backgroundKeys []string
	enabledCount := 0

	rc := &ResourceCache{
		factory:          clusterFactory,
		nsFactories:      nsFactories,
		factoryByKind:    map[string]informers.SharedInformerFactory{},
		indexerByKind:    map[string]cache.Indexer{},
		pagedInformers:   map[string]cache.SharedIndexInformer{},
		changes:          changes,
		stopCh:           stopCh,
		enabledResources: enabled,
		config:           cfg,
		stdlog:           stdlog,
	}

	// Track per-informer sync status, HasSynced funcs, and the informer
	// handle (needed for staggered start).
	type informerEntry struct {
		kind     string
		key      string
		deferred bool
		synced   cache.InformerSynced
		run      func(stopCh <-chan struct{})
	}
	var allEntries []informerEntry

	buildInformer := func(s informerSetup, factory informers.SharedInformerFactory, namespace string) cache.SharedIndexInformer {
		if cfg.ListPageSize > 0 && s.pagedSetup != nil {
			return s.pagedSetup(cfg.Client, namespace, cfg.ListPageSize)
		}
		return s.setup(factory)
	}

	wireInformer := func(inf cache.SharedIndexInformer, s informerSetup, idx int) error {
		var err error
		if s.isEvent {
			err = rc.addEventHandlers(inf, changes)
		} else {
			err = rc.addChangeHandlers(inf, s.kind, changes)
		}
		if err != nil {
			return fmt.Errorf("failed to register %s event handler: %w", s.kind, err)
		}

		// Wire a WatchErrorHandler so reflector-level failures (most
		// notably 403/Unauthorized that the probe didn't predict — e.g.
		// the token expired between probe and watch, or RBAC changed
		// mid-session) are visible in diagnostics. The reflector keeps
		// retrying with exponential backoff regardless; this just makes
		// the failure observable instead of silent.
		key := s.key
		kind := s.kind
		if hErr := inf.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
			rc.recordWatchError(idx, key, kind, err)
		}); hErr != nil {
			// SetWatchErrorHandler only errors after the informer has
			// started, which can't happen here — log and continue.
			stdlog.Printf("Warning: failed to set watch error handler for %s: %v", s.kind, hErr)
		}
		return nil
	}

	for _, s := range setups {
		if !enabled[s.key] {
			continue
		}
		enabledCount++
		idx := len(allEntries) // status index for this logical kind
		scopeNamespaces := resourceScopeNamespaces(s.key, scopes[s.key], cfg.ResourceScopeNamespaces)

		var synced cache.InformerSynced
		var run func(stopCh <-chan struct{})

		if !s.isClusterScoped && len(scopeNamespaces) > 1 {
			informersForKind := make([]cache.SharedIndexInformer, 0, len(scopeNamespaces))
			stores := make([]cache.Indexer, 0, len(scopeNamespaces))
			for _, namespace := range scopeNamespaces {
				factory := nsFactories[namespace]
				inf := buildInformer(s, factory, namespace)
				if err := wireInformer(inf, s, idx); err != nil {
					close(stopCh)
					return nil, err
				}
				informersForKind = append(informersForKind, inf)
				stores = append(stores, inf.GetIndexer())
			}
			rc.indexerByKind[s.key] = &unionIndexer{indexers: stores}
			synced = func() bool {
				for _, inf := range informersForKind {
					if !inf.HasSynced() {
						return false
					}
				}
				return true
			}
			run = func(stopCh <-chan struct{}) {
				for _, inf := range informersForKind {
					go inf.Run(stopCh)
				}
			}
		} else {
			factory := pickFactory(s)
			rc.factoryByKind[s.key] = factory
			namespace := scopes[s.key].Namespace
			if s.isClusterScoped {
				namespace = ""
			}
			inf := buildInformer(s, factory, namespace)
			if cfg.ListPageSize > 0 && s.pagedSetup != nil {
				// Listers must read this informer's store, not the factory's
				// (the factory's informer for this kind is never started).
				rc.pagedInformers[s.key] = inf
			}
			if err := wireInformer(inf, s, idx); err != nil {
				close(stopCh)
				return nil, err
			}
			synced = inf.HasSynced
			run = inf.Run
		}

		isDeferred := deferredTypes[s.key]
		entry := informerEntry{kind: s.kind, key: s.key, deferred: isDeferred, synced: synced, run: run}
		allEntries = append(allEntries, entry)

		if isDeferred && s.isEvent {
			// Events sync independently — they can take 60s+ on large clusters
			// and shouldn't block topology completion or warmup transition.
			backgroundSyncFuncs = append(backgroundSyncFuncs, synced)
			backgroundKeys = append(backgroundKeys, s.key)
		} else if isDeferred {
			deferredSyncFuncs = append(deferredSyncFuncs, synced)
			deferredKeys = append(deferredKeys, s.key)
		} else {
			criticalSyncFuncs = append(criticalSyncFuncs, synced)
		}
	}

	// Initialize per-informer tracking
	statuses := make([]InformerSyncStatus, len(allEntries))
	for i, e := range allEntries {
		statuses[i] = InformerSyncStatus{Kind: e.kind, Key: e.key, Deferred: e.deferred}
	}
	rc.informerStatuses = statuses

	if enabledCount == 0 {
		stdlog.Printf("Warning: No resource types are accessible (all RBAC checks failed)")
		rc.deferredSynced = make(map[string]bool)
		rc.deferredDone = make(chan struct{})
		close(rc.deferredDone)
		rc.syncComplete.Store(true)
		return rc, nil
	}

	// Start critical informers first. Deferred informers are started after
	// Phase 1 completes to reduce concurrent LIST pressure on the API server.
	// On large clusters (300+ nodes), ~10 concurrent LISTs is significantly
	// lighter than ~19, giving the heaviest resource (Pods) more API server
	// bandwidth during the critical path.
	for _, e := range allEntries {
		if !e.deferred {
			go e.run(stopCh)
		}
	}

	if len(backgroundKeys) > 0 {
		stdlog.Printf("Starting resource cache: %d critical + %d deferred + %d background informers (%d total)",
			len(criticalSyncFuncs), len(deferredSyncFuncs), len(backgroundSyncFuncs), enabledCount)
	} else {
		stdlog.Printf("Starting resource cache: %d critical + %d deferred informers (%d total, deferred start after critical sync)",
			len(criticalSyncFuncs), len(deferredSyncFuncs), enabledCount)
	}
	syncStart := time.Now()
	rc.syncStartTime = syncStart

	// Track per-informer sync completion in background goroutines.
	// Each goroutine updates the InformerSyncStatus when its informer syncs.
	for i, e := range allEntries {
		idx := i
		entry := e
		go func() {
			t := time.Now()
			for !entry.synced() {
				select {
				case <-stopCh:
					return
				default:
				}
				time.Sleep(10 * time.Millisecond)
			}
			tag := "critical"
			if entry.deferred {
				tag = "deferred"
			}
			logf("    Informer synced: %-28s %v (%s)", entry.kind, time.Since(t), tag)
			rc.informerMu.Lock()
			rc.informerStatuses[idx].Synced = true
			rc.informerStatuses[idx].SyncedAt = time.Now().Format(time.RFC3339)
			rc.informerMu.Unlock()
		}()
	}

	// Phase 1: Wait for critical informers.
	//
	// Two modes (combinable):
	//   - Patience+MinimalSet: wait for ALL critical until the patience
	//     window elapses. Once elapsed, return as soon as the minimal set
	//     is ready; promote the rest of critical to deferred.
	//   - SyncTimeout: hard upper bound. When it fires, promote everything
	//     still pending and return. Acts as a backstop on the minimal-set
	//     path so a permanently-stuck informer doesn't trap the caller
	//     forever (caller still gets to render with whatever synced).
	useMinimalSet := cfg.PatienceWindow > 0 && len(cfg.MinimalSet) > 0
	timedOut := false
	patienceElapsed := false
	if len(criticalSyncFuncs) > 0 {
		// Build the index of "must-sync" entries for the minimal-set check.
		// Filter to entries that are critical AND in MinimalSet (deferred
		// types or unknown keys don't gate first paint).
		var minimalEntries []informerEntry
		if useMinimalSet {
			knownCritical := make(map[string]bool, len(allEntries))
			for _, e := range allEntries {
				if !e.deferred {
					knownCritical[e.key] = true
				}
				if e.deferred {
					continue
				}
				if cfg.MinimalSet[e.key] {
					minimalEntries = append(minimalEntries, e)
				}
			}
			// Validate MinimalSet keys: typos or kinds not enabled produce a
			// silently-empty minimalEntries → cache returns ~PatienceWindow
			// later with nothing meaningful synced. Surface that loud.
			var unknown []string
			for k := range cfg.MinimalSet {
				if !knownCritical[k] {
					unknown = append(unknown, k)
				}
			}
			if len(unknown) > 0 {
				sort.Strings(unknown)
				stdlog.Printf("WARNING: MinimalSet keys not registered as critical informers (typo or RBAC-denied?): %s",
					strings.Join(unknown, ", "))
			}
			if len(minimalEntries) == 0 {
				stdlog.Printf("WARNING: MinimalSet matched no enabled critical informers; first paint will fire as soon as PatienceWindow elapses regardless of sync state")
			}
		}

		progressTicker := time.NewTicker(1 * time.Second)
		defer progressTicker.Stop()

		var patienceCh <-chan time.Time
		if useMinimalSet {
			patienceTimer := time.NewTimer(cfg.PatienceWindow)
			defer patienceTimer.Stop()
			patienceCh = patienceTimer.C
		}

		var deadlineCh <-chan time.Time
		if cfg.SyncTimeout > 0 {
			deadline := time.NewTimer(cfg.SyncTimeout)
			defer deadline.Stop()
			deadlineCh = deadline.C
		}

		// emitProgress reports current sync progress to the application
		// callback (typically wired to the connection's progressMessage).
		emitProgress := func(minimalReady bool) {
			if cfg.SyncProgress == nil {
				return
			}
			rc.informerMu.RLock()
			var synced, total int
			for _, s := range rc.informerStatuses {
				if s.Deferred {
					continue
				}
				total++
				if s.Synced {
					synced++
				}
			}
			rc.informerMu.RUnlock()
			cfg.SyncProgress(synced, total, minimalReady)
		}

		minimalReady := func() bool {
			for _, e := range minimalEntries {
				if !e.synced() {
					return false
				}
			}
			return true
		}

		emitProgress(false)

		// Set of minimal-set keys for the post-patience log discrimination.
		minimalKindSet := make(map[string]bool, len(minimalEntries))
		for _, e := range minimalEntries {
			minimalKindSet[e.kind] = true
		}

		for {
			allSynced := true
			for _, fn := range criticalSyncFuncs {
				if !fn() {
					allSynced = false
					break
				}
			}
			if allSynced {
				break
			}

			if patienceElapsed && useMinimalSet && minimalReady() {
				break
			}

			select {
			case <-stopCh:
				return nil, fmt.Errorf("failed to sync critical resource caches")
			case <-patienceCh:
				patienceElapsed = true
				patienceCh = nil // disable further receives on the drained timer channel
			case <-deadlineCh:
				timedOut = true
			case <-progressTicker.C:
				counts := rc.GetKindObjectCounts()
				rc.informerMu.RLock()
				var synced, pendingParts, minimalPending []string
				for _, s := range rc.informerStatuses {
					if s.Deferred {
						continue
					}
					if s.Synced {
						synced = append(synced, s.Kind)
					} else {
						n := counts[s.Kind]
						pendingParts = append(pendingParts, fmt.Sprintf("%s(%d)", s.Kind, n))
						if minimalKindSet[s.Kind] {
							minimalPending = append(minimalPending, s.Kind)
						}
					}
				}
				rc.informerMu.RUnlock()
				if len(pendingParts) > 0 {
					// After patience elapses, the actual blocker for first paint
					// is the minimal-set subset, not all of critical — surface it
					// distinctly so operators know which kind is holding render.
					if patienceElapsed && len(minimalPending) > 0 {
						stdlog.Printf("First-paint blocked: %d/%d minimal-set kinds still syncing (%.0fs elapsed) — pending: %s",
							len(minimalPending), len(minimalEntries), time.Since(syncStart).Seconds(), strings.Join(minimalPending, ", "))
					} else {
						stdlog.Printf("Critical sync progress: %d/%d synced (%.0fs elapsed) — pending: %s",
							len(synced), len(synced)+len(pendingParts), time.Since(syncStart).Seconds(), strings.Join(pendingParts, ", "))
					}
				}
				emitProgress(useMinimalSet && patienceElapsed && minimalReady())
			default:
				time.Sleep(100 * time.Millisecond)
			}
			if timedOut {
				break
			}
		}
	}

	// Reclassify any critical informer still pending as deferred so the
	// cache can return. No-op on the all-synced path.
	var promoted []string
	rc.informerMu.Lock()
	for i, e := range allEntries {
		if e.deferred || e.synced() {
			continue
		}
		promoted = append(promoted, e.kind)
		deferredSyncFuncs = append(deferredSyncFuncs, e.synced)
		deferredKeys = append(deferredKeys, e.key)
		rc.informerStatuses[i].Deferred = true
	}
	rc.informerMu.Unlock()

	switch {
	case timedOut:
		stdlog.Printf("WARNING: Critical sync timed out after %v — promoting %d informers to deferred: %s",
			cfg.SyncTimeout, len(promoted), strings.Join(promoted, ", "))
		stdlog.Printf("UI will render with partial data; promoted informers continue syncing in background")
		logf("    Phase 1 sync TIMED OUT (%d critical, %d promoted to deferred): %v",
			len(criticalSyncFuncs), len(promoted), time.Since(syncStart))
		rc.promotedKinds = promoted
	case patienceElapsed && len(promoted) > 0:
		stdlog.Printf("First-paint ready after %v: minimal set synced; %d slower informers continue in background: %s",
			time.Since(syncStart), len(promoted), strings.Join(promoted, ", "))
		logf("    Phase 1 minimal-set sync (%d/%d critical, %d still loading): %v",
			len(criticalSyncFuncs)-len(promoted), len(criticalSyncFuncs), len(promoted), time.Since(syncStart))
		rc.promotedKinds = promoted
	default:
		logf("    Phase 1 sync (%d critical informers): %v", len(criticalSyncFuncs), time.Since(syncStart))
		stdlog.Printf("Critical resource caches synced in %v — UI can render", time.Since(syncStart))
	}

	if cfg.SyncProgress != nil {
		// Count via e.synced() (same source as the Phase 1 loop) rather than
		// informerStatuses[].Synced, which is set asynchronously by per-informer
		// tracking goroutines and can lag by ~10ms after HasSynced() flips. On
		// the all-synced happy path the lag would otherwise produce synced<total
		// here even though we just exited the loop on allSynced — surfaced to
		// callers as a misleading "showing partial" final message.
		rc.informerMu.RLock()
		var synced, total int
		for i, e := range allEntries {
			if rc.informerStatuses[i].Deferred {
				continue
			}
			total++
			if e.synced() {
				synced++
			}
		}
		rc.informerMu.RUnlock()
		cfg.SyncProgress(synced, total, true)
	}

	// Log per-type resource counts for startup diagnostics
	if counts := rc.GetKindObjectCounts(); len(counts) > 0 {
		total := 0
		var parts []string
		for kind, count := range counts {
			if count > 0 {
				parts = append(parts, fmt.Sprintf("%s:%d", kind, count))
				total += count
			}
		}
		sort.Strings(parts)
		stdlog.Printf("Resource breakdown (%d total): %s", total, strings.Join(parts, ", "))
	}

	rc.syncComplete.Store(true)

	// Build deferred tracking state (includes both deferred and background keys)
	allDeferredKeys := append(append([]string{}, deferredKeys...), backgroundKeys...)
	deferredSynced := make(map[string]bool, len(allDeferredKeys))
	for _, k := range allDeferredKeys {
		deferredSynced[k] = false
	}
	deferredDone := make(chan struct{})
	rc.deferredSynced = deferredSynced
	rc.deferredDone = deferredDone

	// Phase 2: Start deferred informers now that critical sync is done,
	// then wait for them in background. This staggers the API server load.
	for _, e := range allEntries {
		if e.deferred {
			go e.run(stopCh)
		}
	}

	if len(deferredSyncFuncs) > 0 {
		go func() {
			deferredStart := time.Now()
			progressTicker := time.NewTicker(5 * time.Second)
			defer progressTicker.Stop()

			var deadlineCh <-chan time.Time
			if cfg.DeferredSyncTimeout > 0 {
				t := time.NewTimer(cfg.DeferredSyncTimeout)
				defer t.Stop()
				deadlineCh = t.C
			}

			timedOut := false
			for {
				// Mark each informer synced the moment its own HasSynced() is
				// true. A permanently-failing informer (e.g. HPA autoscaling/v2
				// on K8s <1.23) must not block siblings from becoming ready.
				rc.deferredMu.Lock()
				allSynced := true
				for i, fn := range deferredSyncFuncs {
					k := deferredKeys[i]
					if rc.deferredSynced[k] {
						continue
					}
					if fn() {
						rc.deferredSynced[k] = true
					} else {
						allSynced = false
					}
				}
				rc.deferredMu.Unlock()
				if allSynced {
					break
				}

				select {
				case <-stopCh:
					rc.deferredFailed.Store(true)
					stdlog.Printf("ERROR: Deferred resource cache sync aborted after %v", time.Since(deferredStart))
					close(deferredDone)
					return
				case <-deadlineCh:
					timedOut = true
				case <-progressTicker.C:
					counts := rc.GetKindObjectCounts()
					rc.informerMu.RLock()
					var synced, pendingParts []string
					for _, s := range rc.informerStatuses {
						if !s.Deferred {
							continue
						}
						// Skip background informers (Events) in deferred progress
						if slices.Contains(backgroundKeys, s.Key) {
							continue
						}
						if s.Synced {
							synced = append(synced, s.Kind)
						} else {
							n := counts[s.Kind]
							pendingParts = append(pendingParts, fmt.Sprintf("%s(%d)", s.Kind, n))
						}
					}
					rc.informerMu.RUnlock()
					stdlog.Printf("Deferred sync progress: %d/%d synced (%.0fs elapsed) — pending: %s",
						len(synced), len(synced)+len(pendingParts), time.Since(deferredStart).Seconds(), strings.Join(pendingParts, ", "))
				default:
					time.Sleep(100 * time.Millisecond)
				}

				if timedOut {
					break
				}
			}

			if timedOut {
				// Stop waiting on stragglers. deferredFailed is the "give up"
				// signal read by IsDeferredPending — stragglers start reporting
				// not-pending, so HTTP handlers return 403 instead of perpetual
				// 503. Informers that already synced keep their own
				// deferredSynced[k]=true and continue serving normally.
				rc.deferredMu.RLock()
				var pending []string
				for _, k := range deferredKeys {
					if !rc.deferredSynced[k] {
						pending = append(pending, k)
					}
				}
				rc.deferredMu.RUnlock()
				rc.deferredFailed.Store(true)
				stdlog.Printf("WARNING: Deferred sync timed out after %v; %d informer(s) never synced: %s. "+
					"These resources will return 403 to API consumers. Common cause: the corresponding API "+
					"version isn't served on this cluster (check `kubectl api-resources | grep <resource>`).",
					cfg.DeferredSyncTimeout, len(pending), strings.Join(pending, ", "))
			} else {
				logf("    Phase 2 sync (%d deferred informers): %v", len(deferredSyncFuncs), time.Since(deferredStart))
				stdlog.Printf("Deferred resource caches synced in %v (total: %v)", time.Since(deferredStart), time.Since(syncStart))
			}
			close(deferredDone)
		}()
	} else {
		close(deferredDone)
	}

	// Background informers (Events) sync independently — they can take 60s+
	// on large clusters and shouldn't block topology/warmup completion.
	if len(backgroundSyncFuncs) > 0 {
		go func() {
			bgStart := time.Now()
			if cache.WaitForCacheSync(stopCh, backgroundSyncFuncs...) {
				rc.deferredMu.Lock()
				for _, k := range backgroundKeys {
					rc.deferredSynced[k] = true
				}
				rc.deferredMu.Unlock()
				stdlog.Printf("Background Events sync complete in %v", time.Since(bgStart))
			} else {
				rc.deferredFailed.Store(true)
				stdlog.Printf("WARNING: Background Events sync failed after %v", time.Since(bgStart))
			}
		}()
	}

	return rc, nil
}

// InformerResourceKeys returns the resource-type keys that have a typed
// informer setup in this package. Read-only introspection for cross-package
// alignment tests (e.g. internal/k8s asserting that every capability field
// has either a typed informer here or a documented dynamic-cache exception).
// Not intended as a stable API for application use.
func InformerResourceKeys() []string {
	setups := buildInformerSetups()
	keys := make([]string, 0, len(setups))
	for _, s := range setups {
		keys = append(keys, s.key)
	}
	return keys
}

// buildInformerSetups returns the table-driven informer setup list. Each
// entry's setup func takes the factory it should be created from — caller
// picks the cluster-wide or a namespace-scoped factory based on the kind's
// resolved access scope.
func buildInformerSetups() []informerSetup {
	type entry = informerSetup
	mk := func(key, kind string, isEvent, clusterScoped bool, fn func(f informers.SharedInformerFactory) cache.SharedIndexInformer) entry {
		return entry{key: key, kind: kind, setup: fn, isEvent: isEvent, isClusterScoped: clusterScoped}
	}
	// withPaging marks a high-cardinality kind as paginatable: when
	// CacheConfig.ListPageSize > 0, its initial LIST is fetched in pages.
	withPaging := func(e entry, paged func(client kubernetes.Interface, namespace string, pageSize int64) cache.SharedIndexInformer) entry {
		e.pagedSetup = paged
		return e
	}
	return []informerSetup{
		mk(Services, "Service", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().Services().Informer()
		}),
		withPaging(mk(Pods, "Pod", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().Pods().Informer()
		}), func(client kubernetes.Interface, ns string, pageSize int64) cache.SharedIndexInformer {
			return newPagedInformer(&corev1.Pod{}, pageSize,
				func(ctx context.Context, o metav1.ListOptions) (apiruntime.Object, error) {
					return client.CoreV1().Pods(ns).List(ctx, o)
				},
				func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
					return client.CoreV1().Pods(ns).Watch(ctx, o)
				})
		}),
		mk(Nodes, "Node", false, true, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().Nodes().Informer()
		}),
		mk(Namespaces, "Namespace", false, true, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().Namespaces().Informer()
		}),
		mk(ConfigMaps, "ConfigMap", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().ConfigMaps().Informer()
		}),
		mk(Secrets, "Secret", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().Secrets().Informer()
		}),
		mk(Events, "Event", true, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().Events().Informer()
		}),
		mk(PersistentVolumeClaims, "PersistentVolumeClaim", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().PersistentVolumeClaims().Informer()
		}),
		mk(PersistentVolumes, "PersistentVolume", false, true, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().PersistentVolumes().Informer()
		}),
		mk(Deployments, "Deployment", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Apps().V1().Deployments().Informer()
		}),
		mk(DaemonSets, "DaemonSet", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Apps().V1().DaemonSets().Informer()
		}),
		mk(StatefulSets, "StatefulSet", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Apps().V1().StatefulSets().Informer()
		}),
		withPaging(mk(ReplicaSets, "ReplicaSet", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Apps().V1().ReplicaSets().Informer()
		}), func(client kubernetes.Interface, ns string, pageSize int64) cache.SharedIndexInformer {
			return newPagedInformer(&appsv1.ReplicaSet{}, pageSize,
				func(ctx context.Context, o metav1.ListOptions) (apiruntime.Object, error) {
					return client.AppsV1().ReplicaSets(ns).List(ctx, o)
				},
				func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
					return client.AppsV1().ReplicaSets(ns).Watch(ctx, o)
				})
		}),
		mk(Ingresses, "Ingress", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Networking().V1().Ingresses().Informer()
		}),
		mk(IngressClasses, "IngressClass", false, true, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Networking().V1().IngressClasses().Informer()
		}),
		mk(Jobs, "Job", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Batch().V1().Jobs().Informer()
		}),
		mk(CronJobs, "CronJob", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Batch().V1().CronJobs().Informer()
		}),
		mk(HorizontalPodAutoscalers, "HorizontalPodAutoscaler", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Autoscaling().V2().HorizontalPodAutoscalers().Informer()
		}),
		mk(StorageClasses, "StorageClass", false, true, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Storage().V1().StorageClasses().Informer()
		}),
		mk(PodDisruptionBudgets, "PodDisruptionBudget", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Policy().V1().PodDisruptionBudgets().Informer()
		}),
		mk(NetworkPolicies, "NetworkPolicy", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Networking().V1().NetworkPolicies().Informer()
		}),
		mk(ServiceAccounts, "ServiceAccount", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().ServiceAccounts().Informer()
		}),
		mk(Roles, "Role", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Rbac().V1().Roles().Informer()
		}),
		mk(ClusterRoles, "ClusterRole", false, true, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Rbac().V1().ClusterRoles().Informer()
		}),
		mk(RoleBindings, "RoleBinding", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Rbac().V1().RoleBindings().Informer()
		}),
		mk(ClusterRoleBindings, "ClusterRoleBinding", false, true, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Rbac().V1().ClusterRoleBindings().Informer()
		}),
		mk(LimitRanges, "LimitRange", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().LimitRanges().Informer()
		}),
		mk(ResourceQuotas, "ResourceQuota", false, false, func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().ResourceQuotas().Informer()
		}),
	}
}

// addChangeHandlers registers event handlers for non-Event resource changes.
func (rc *ResourceCache) addChangeHandlers(inf cache.SharedIndexInformer, kind string, ch chan<- ResourceChange) error {
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			rc.enqueueChange(ch, kind, obj, nil, OpAdd)
		},
		UpdateFunc: func(oldObj, newObj any) {
			rc.enqueueChange(ch, kind, newObj, oldObj, OpUpdate)
		},
		DeleteFunc: func(obj any) {
			rc.enqueueChange(ch, kind, obj, nil, OpDelete)
		},
	})
	return err
}

// addEventHandlers registers special handlers for K8s Events.
// Events use a separate path: no noisy filtering, no diff computation.
func (rc *ResourceCache) addEventHandlers(inf cache.SharedIndexInformer, ch chan<- ResourceChange) error {
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			rc.enqueueEvent(ch, obj, OpAdd)
		},
		UpdateFunc: func(oldObj, newObj any) {
			rc.enqueueEvent(ch, newObj, OpUpdate)
		},
		DeleteFunc: func(obj any) {
			rc.enqueueEvent(ch, obj, OpDelete)
		},
	})
	return err
}

// enqueueChange handles non-Event resource change notifications.
func (rc *ResourceCache) enqueueChange(ch chan<- ResourceChange, kind string, obj any, oldObj any, op string) {
	meta, ok := obj.(metav1.Object)
	if !ok {
		if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			meta, ok = tombstone.Obj.(metav1.Object)
			if !ok {
				rc.stdlog.Printf("Warning: tombstone contained non-metav1.Object for %s %s", kind, op)
				return
			}
			obj = tombstone.Obj
		} else {
			return
		}
	}

	ns := meta.GetNamespace()
	name := meta.GetName()
	uid := string(meta.GetUID())

	// Track event received (before any filtering)
	if rc.config.OnReceived != nil {
		rc.safeCallback("OnReceived", func() { rc.config.OnReceived(kind) })
	}

	// Check if noisy — skip OnChange callback and don't send to changes channel.
	// Noisy resources (Lease, Endpoints, EndpointSlice updates, etc.) fire constantly
	// and don't affect topology or produce meaningful k8s_event broadcasts.
	skipCallback := false
	isNoisy := false
	if rc.config.IsNoisyResource != nil && rc.config.IsNoisyResource(kind, name, op) {
		skipCallback = true
		isNoisy = true
		if rc.config.OnDrop != nil {
			rc.config.OnDrop(kind, ns, name, "noisy_filter", op)
		}
	}

	// SuppressInitialAdds: during initial sync, skip OnChange for adds
	if op == "add" && rc.config.SuppressInitialAdds && !rc.syncComplete.Load() {
		skipCallback = true
	}

	// Compute diff for updates
	var diff *DiffInfo
	if op == "update" && oldObj != nil && obj != nil && rc.config.ComputeDiff != nil {
		diff = rc.config.ComputeDiff(kind, oldObj, obj)
	}

	change := ResourceChange{
		Kind:      kind,
		Namespace: ns,
		Name:      name,
		UID:       uid,
		Operation: op,
		Diff:      diff,
	}

	// Fire OnChange callback (before channel send, matching existing behavior)
	if !skipCallback && rc.config.OnChange != nil {
		rc.safeCallback("OnChange", func() { rc.config.OnChange(change, obj, oldObj) })
	}

	// Non-blocking send to changes channel (skip noisy resources entirely —
	// they don't affect topology and would just trigger unnecessary rebuilds)
	if !isNoisy {
		select {
		case ch <- change:
		default:
			if rc.config.OnDrop != nil {
				rc.config.OnDrop(kind, ns, name, "channel_full", op)
			} else {
				rc.stdlog.Printf("Warning: change channel full, dropped %s %s/%s op=%s", kind, ns, name, op)
			}
		}
	}
}

// enqueueEvent handles K8s Event resource changes (separate path).
func (rc *ResourceCache) enqueueEvent(ch chan<- ResourceChange, obj any, op string) {
	meta, ok := obj.(metav1.Object)
	if !ok {
		if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			meta, ok = tombstone.Obj.(metav1.Object)
			if !ok {
				rc.stdlog.Printf("Warning: tombstone contained non-metav1.Object for Event %s", op)
				return
			}
			obj = tombstone.Obj
		} else {
			return
		}
	}

	ns := meta.GetNamespace()
	name := meta.GetName()
	uid := string(meta.GetUID())

	// Fire OnEventChange callback
	if rc.config.OnEventChange != nil {
		rc.safeCallback("OnEventChange", func() { rc.config.OnEventChange(obj, op) })
	}

	change := ResourceChange{
		Kind:      "Event",
		Namespace: ns,
		Name:      name,
		UID:       uid,
		Operation: op,
	}

	select {
	case ch <- change:
	default:
		if rc.config.OnDrop != nil {
			rc.config.OnDrop("Event", ns, name, "channel_full", op)
		} else {
			rc.stdlog.Printf("Warning: change channel full, dropped Event %s/%s op=%s", ns, name, op)
		}
	}
}

// safeCallback invokes fn with panic recovery to protect informer goroutines.
func (rc *ResourceCache) safeCallback(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			rc.stdlog.Printf("ERROR: k8score %s callback panicked: %v\n%s", name, r, buf[:n])
		}
	}()
	fn()
}

// recordWatchError is invoked from each informer's WatchErrorHandler when its
// reflector drops its connection. It updates the per-informer status so the
// failure surfaces in diagnostics, and logs the first 403/401 — these signal
// that the access probe and the actual list/watch disagree (token rotation,
// RBAC change mid-session, or webhook authorizer flakiness).
//
// The reflector continues retrying with exponential backoff regardless; this
// hook just makes the failure observable. Cap the recorded error string to
// keep the diagnostics payload small.
func (rc *ResourceCache) recordWatchError(idx int, key, kind string, err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	const maxLen = 256
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "…"
	}
	forbidden := isWatchForbiddenErr(err)

	rc.informerMu.Lock()
	if idx >= 0 && idx < len(rc.informerStatuses) {
		s := &rc.informerStatuses[idx]
		firstForbidden := forbidden && !s.ForbiddenSeen
		s.LastError = msg
		s.LastErrorAt = time.Now().Format(time.RFC3339)
		if forbidden {
			s.ForbiddenSeen = true
		}
		if firstForbidden {
			rc.stdlog.Printf("WARNING: informer %s saw 403/Unauthorized — probe and reality disagree (key=%s): %v",
				kind, key, err)
		}
	}
	rc.informerMu.Unlock()
}

// isWatchForbiddenErr returns true when err signals 403/401 from the
// apiserver. The reflector wraps the typed error in an unstructured
// status error, so apierrors helpers (which need an *apierrors.StatusError)
// can miss it — fall back to substring matching when type-detection fails.
//
// Kept here (rather than reusing the apierrors-aware helper in capabilities.go)
// because pkg/k8score must not import internal/.
func isWatchForbiddenErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "forbidden") || strings.Contains(msg, "unauthorized")
}

// Stop initiates a non-blocking shutdown of the cache.
func (rc *ResourceCache) Stop() {
	if rc == nil {
		return
	}
	rc.stopOnce.Do(func() {
		rc.stdlog.Println("Stopping resource cache")
		close(rc.stopCh)
		go func() {
			done := make(chan struct{})
			go func() {
				rc.factory.Shutdown()
				for _, f := range rc.nsFactories {
					f.Shutdown()
				}
				close(done)
			}()
			select {
			case <-done:
				rc.stdlog.Println("Resource cache factory shutdown complete")
			case <-time.After(5 * time.Second):
				rc.stdlog.Println("Resource cache factory shutdown taking >5s, abandoning")
			}
		}()
	})
}

// Changes returns a read-only channel for resource change notifications.
func (rc *ResourceCache) Changes() <-chan ResourceChange {
	if rc == nil {
		return nil
	}
	return rc.changes
}

// IsKindClusterWide reports whether the informer for the given resource (plural
// lowercase name, e.g. "services") is enabled and watches ALL namespaces. When
// true, the lister's contents are authoritative cluster-wide; when false
// (disabled or namespace-scoped fallback), reads only cover a subset of
// namespaces, so callers must not infer cluster-wide absence from a miss.
func (rc *ResourceCache) IsKindClusterWide(resource string) bool {
	if rc == nil {
		return false
	}
	s, ok := rc.config.ResourceScopes[resource]
	return ok && s.Enabled && s.Namespace == "" && len(rc.config.ResourceScopeNamespaces[resource]) == 0
}

// ChangesRaw returns the bidirectional channel for internal use.
func (rc *ResourceCache) ChangesRaw() chan ResourceChange {
	if rc == nil {
		return nil
	}
	return rc.changes
}

// PromotedKinds returns the list of resource kinds that were promoted from
// critical to deferred at first paint. Empty if sync completed normally.
// This is a snapshot from cache construction and does NOT shrink as
// promoted informers eventually sync — use PendingPromotedKinds for the
// live "still loading" view (e.g. for UI banners).
func (rc *ResourceCache) PromotedKinds() []string {
	if rc == nil {
		return nil
	}
	return rc.promotedKinds
}

// PendingPromotedKinds returns the subset of PromotedKinds whose informers
// have not yet completed their initial sync. Drains to empty as the
// background informers finish, so a UI bound to this method shows a
// truthful "still loading" indicator.
func (rc *ResourceCache) PendingPromotedKinds() []string {
	if rc == nil || len(rc.promotedKinds) == 0 {
		return nil
	}
	rc.informerMu.RLock()
	defer rc.informerMu.RUnlock()
	syncedByKind := make(map[string]bool, len(rc.informerStatuses))
	for _, s := range rc.informerStatuses {
		if s.Synced {
			syncedByKind[s.Kind] = true
		}
	}
	var pending []string
	for _, k := range rc.promotedKinds {
		if !syncedByKind[k] {
			pending = append(pending, k)
		}
	}
	return pending
}

// IsSyncComplete returns true after the initial critical informer sync.
func (rc *ResourceCache) IsSyncComplete() bool {
	if rc == nil {
		return false
	}
	return rc.syncComplete.Load()
}

// IsDeferredSynced returns true when all deferred (non-background) informers have
// completed sync. Background informers (e.g. Events) sync independently and are
// not included. Returns false if still syncing or if sync failed.
func (rc *ResourceCache) IsDeferredSynced() bool {
	if rc == nil {
		return false
	}
	select {
	case <-rc.deferredDone:
		return !rc.deferredFailed.Load()
	default:
		return false
	}
}

// DeferredDone returns a channel that is closed when all deferred (non-background)
// informers have completed their initial sync. Background informers sync independently.
func (rc *ResourceCache) DeferredDone() <-chan struct{} {
	if rc == nil {
		return nil
	}
	return rc.deferredDone
}

// GetSyncStatus returns the current sync status of all informers for diagnostics.
// Safe to call at any time, including during sync.
func (rc *ResourceCache) GetSyncStatus() CacheSyncStatus {
	if rc == nil {
		return CacheSyncStatus{Phase: SyncPhaseNotStarted}
	}

	rc.informerMu.RLock()
	statuses := make([]InformerSyncStatus, len(rc.informerStatuses))
	copy(statuses, rc.informerStatuses)
	rc.informerMu.RUnlock()

	// Enrich with live item counts from listers
	counts := rc.GetKindObjectCounts()
	for i := range statuses {
		if n, ok := counts[statuses[i].Kind]; ok {
			statuses[i].Items = n
		}
	}

	var critTotal, critSynced, defTotal, defSynced int
	var pendingCritical, pendingDeferred []string
	for _, s := range statuses {
		if s.Deferred {
			defTotal++
			if s.Synced {
				defSynced++
			} else {
				pendingDeferred = append(pendingDeferred, s.Kind)
			}
		} else {
			critTotal++
			if s.Synced {
				critSynced++
			} else {
				pendingCritical = append(pendingCritical, s.Kind)
			}
		}
	}

	var phase SyncPhase
	switch {
	case rc.syncStartTime.IsZero():
		phase = SyncPhaseNotStarted
	case !rc.syncComplete.Load():
		phase = SyncPhaseCritical
	case !rc.IsDeferredSynced():
		phase = SyncPhaseDeferred
	default:
		phase = SyncPhaseComplete
	}

	result := CacheSyncStatus{
		Phase:           phase,
		ElapsedSec:      time.Since(rc.syncStartTime).Seconds(),
		CriticalTotal:   critTotal,
		CriticalSynced:  critSynced,
		DeferredTotal:   defTotal,
		DeferredSynced:  defSynced,
		Informers:       statuses,
		PendingCritical: pendingCritical,
		PendingDeferred: pendingDeferred,
		PromotedKinds:   rc.promotedKinds,
	}
	if !rc.syncStartTime.IsZero() {
		result.SyncStarted = rc.syncStartTime.Format(time.RFC3339)
	}
	return result
}

// GetEnabledResources returns a copy of the enabled resources map.
func (rc *ResourceCache) GetEnabledResources() map[string]bool {
	if rc == nil {
		return nil
	}
	result := make(map[string]bool, len(rc.enabledResources))
	maps.Copy(result, rc.enabledResources)
	return result
}

// GetResourceCount returns total cached resources across all enabled non-Event listers.
func (rc *ResourceCache) GetResourceCount() int {
	if rc == nil {
		return 0
	}
	counts := rc.GetKindObjectCounts()
	total := 0
	for kind, n := range counts {
		if kind == "Event" {
			continue // Events are not counted as "resources"
		}
		total += n
	}
	return total
}

// kindLister maps a Kind name to a lister accessor for table-driven counting.
type kindLister struct {
	kind   string
	group  string // API group (empty for core, e.g. "apps", "batch", "networking.k8s.io")
	lister func(rc *ResourceCache) any
}

// allKindListers is the table of all resource kinds and their lister accessors.
var allKindListers = []kindLister{
	{"Pod", "", func(rc *ResourceCache) any { return rc.Pods() }},
	{"Service", "", func(rc *ResourceCache) any { return rc.Services() }},
	{"Node", "", func(rc *ResourceCache) any { return rc.Nodes() }},
	{"Namespace", "", func(rc *ResourceCache) any { return rc.Namespaces() }},
	{"ConfigMap", "", func(rc *ResourceCache) any { return rc.ConfigMaps() }},
	{"Secret", "", func(rc *ResourceCache) any { return rc.Secrets() }},
	{"Event", "", func(rc *ResourceCache) any { return rc.Events() }},
	{"PersistentVolumeClaim", "", func(rc *ResourceCache) any { return rc.PersistentVolumeClaims() }},
	{"PersistentVolume", "", func(rc *ResourceCache) any { return rc.PersistentVolumes() }},
	{"Deployment", "apps", func(rc *ResourceCache) any { return rc.Deployments() }},
	{"DaemonSet", "apps", func(rc *ResourceCache) any { return rc.DaemonSets() }},
	{"StatefulSet", "apps", func(rc *ResourceCache) any { return rc.StatefulSets() }},
	{"ReplicaSet", "apps", func(rc *ResourceCache) any { return rc.ReplicaSets() }},
	{"Ingress", "networking.k8s.io", func(rc *ResourceCache) any { return rc.Ingresses() }},
	{"IngressClass", "networking.k8s.io", func(rc *ResourceCache) any { return rc.IngressClasses() }},
	{"Job", "batch", func(rc *ResourceCache) any { return rc.Jobs() }},
	{"CronJob", "batch", func(rc *ResourceCache) any { return rc.CronJobs() }},
	{"HorizontalPodAutoscaler", "autoscaling", func(rc *ResourceCache) any { return rc.HorizontalPodAutoscalers() }},
	{"StorageClass", "storage.k8s.io", func(rc *ResourceCache) any { return rc.StorageClasses() }},
	{"PodDisruptionBudget", "policy", func(rc *ResourceCache) any { return rc.PodDisruptionBudgets() }},
	{"NetworkPolicy", "networking.k8s.io", func(rc *ResourceCache) any { return rc.NetworkPolicies() }},
	{"ServiceAccount", "", func(rc *ResourceCache) any { return rc.ServiceAccounts() }},
	{"Role", "rbac.authorization.k8s.io", func(rc *ResourceCache) any { return rc.Roles() }},
	{"ClusterRole", "rbac.authorization.k8s.io", func(rc *ResourceCache) any { return rc.ClusterRoles() }},
	{"RoleBinding", "rbac.authorization.k8s.io", func(rc *ResourceCache) any { return rc.RoleBindings() }},
	{"ClusterRoleBinding", "rbac.authorization.k8s.io", func(rc *ResourceCache) any { return rc.ClusterRoleBindings() }},
	{"LimitRange", "", func(rc *ResourceCache) any { return rc.LimitRanges() }},
	{"ResourceQuota", "", func(rc *ResourceCache) any { return rc.ResourceQuotas() }},
}

// AllKindListers returns the table of all resource kinds with their group and lister.
// Used by the resource-counts endpoint to enumerate typed resources.
func AllKindListers() []kindLister {
	return allKindListers
}

// Kind returns the resource kind name.
func (kl kindLister) Kind() string { return kl.kind }

// Group returns the API group (empty for core resources).
func (kl kindLister) Group() string { return kl.group }

// Lister returns the lister accessor function.
func (kl kindLister) Lister() func(rc *ResourceCache) any { return kl.lister }

// CountKey returns the key used in resource-counts responses: "group/Kind" or just "Kind" for core.
func (kl kindLister) CountKey() string {
	if kl.group != "" {
		return kl.group + "/" + kl.kind
	}
	return kl.kind
}

// GetKindObjectCounts returns the number of cached objects per resource kind.
// Only includes kinds that are enabled. Returns nil if cache is nil.
func (rc *ResourceCache) GetKindObjectCounts() map[string]int {
	if rc == nil {
		return nil
	}
	counts := make(map[string]int)
	for _, kl := range allKindListers {
		l := kl.lister(rc)
		if l == nil {
			continue
		}
		n := listCount(l)
		if n > 0 {
			counts[kl.kind] = n
		}
	}
	return counts
}

// isEnabled returns true if the resource type has an informer running.
func (rc *ResourceCache) isEnabled(key string) bool {
	if rc == nil || rc.enabledResources == nil {
		return false
	}
	return rc.enabledResources[key]
}

// isReady returns true if the resource is enabled and, if deferred, synced.
func (rc *ResourceCache) isReady(key string) bool {
	if !rc.isEnabled(key) {
		return false
	}
	if rc.config.DeferredTypes == nil || !rc.config.DeferredTypes[key] {
		return true
	}
	rc.deferredMu.RLock()
	defer rc.deferredMu.RUnlock()
	return rc.deferredSynced[key]
}

// IsDeferredPending returns true when the resource type passed RBAC checks
// (informer is enabled) but deferred sync has not completed yet. Callers
// can use this to distinguish "no permission" (return 403) from "not ready
// yet" (return 503) when a lister returns nil.
// Returns false once deferred sync has permanently failed (avoids infinite spinner).
func (rc *ResourceCache) IsDeferredPending(key string) bool {
	if rc == nil {
		return false
	}
	if !rc.isEnabled(key) {
		return false
	}
	if rc.config.DeferredTypes == nil || !rc.config.DeferredTypes[key] {
		return false
	}
	if rc.deferredFailed.Load() {
		return false
	}
	rc.deferredMu.RLock()
	defer rc.deferredMu.RUnlock()
	return !rc.deferredSynced[key]
}
