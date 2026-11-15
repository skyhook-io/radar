// Command relparity connects to a cluster as Radar does and answers every
// cached object's Related Resources two ways: from the all-namespace
// relationship graph, as the drawer does today, and from the relation
// families in pkg/relations, as the drawer will. It reports every field where
// the two answers differ, how long each answer took, and what the graph
// costs to build and hold. It only reads the cluster.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/homedir"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

type subject struct {
	ref resourceid.Ref
	obj k8sruntime.Object
}

// timing is one kind's answer times; the micros are p50, p95 and max.
type timing struct {
	Count    int       `json:"count"`
	Before   []int64   `json:"-"`
	After    []int64   `json:"-"`
	BeforeUS []float64 `json:"beforeMicros"`
	AfterUS  []float64 `json:"afterMicros"`
}

type diffKey struct {
	Kind, Field, Side, RefKind string
}

type diffGroup struct {
	Kind     string   `json:"subjectKind"`
	Field    string   `json:"field"`
	Side     string   `json:"onlyIn"`
	RefKind  string   `json:"refKind"`
	Subjects int      `json:"subjects"`
	Refs     int      `json:"refs"`
	Examples []string `json:"examples"`
}

type report struct {
	Context        string             `json:"context"`
	Started        time.Time          `json:"started"`
	Objects        int                `json:"objects"`
	Kinds          map[string]int     `json:"kinds"`
	WatchedCRDs    int                `json:"watchedCRDKinds"`
	GraphBuildMS   []float64          `json:"graphBuildMillis"`
	GraphIndexMS   float64            `json:"graphIndexMillis"`
	GraphNodes     int                `json:"graphNodes"`
	GraphEdges     int                `json:"graphEdges"`
	GraphHeldMiB   float64            `json:"graphHeldMiB"`
	CacheHeapMiB   float64            `json:"cacheHeapMiB"`
	Timings        map[string]*timing `json:"timings"`
	SubjectsEqual  int                `json:"subjectsEqual"`
	SubjectsDiffer int                `json:"subjectsDiffer"`
	Diffs          []diffGroup        `json:"diffs"`
	FamilyGaps     map[string]int     `json:"familyGaps"`

	// A user who may read half the namespaces and everything cluster-scoped,
	// asked about the objects they may read: today's per-user graph copy
	// against the families answering with their access.
	RestrictedNamespaces int                `json:"restrictedNamespaces"`
	RestrictedTimings    map[string]*timing `json:"restrictedTimings"`
	RestrictedEqual      int                `json:"restrictedEqual"`
	RestrictedDiffer     int                `json:"restrictedDiffer"`
	RestrictedDiffs      []diffGroup        `json:"restrictedDiffs"`

	ManagedBy            map[string]int      `json:"managedBy"`
	Examples             map[string][]string `json:"managedByExamples"`
	ManagedByUnexplained int                 `json:"managedByUnexplained"`
}

// namespacesAccess may read the namespaces it lists and anything
// cluster-scoped.
type namespacesAccess []string

func (n namespacesAccess) CanRead(_ resourceid.GroupKind, namespace string) ownership.Decision {
	if namespace == "" || slices.Contains(n, namespace) {
		return ownership.Allowed
	}
	return ownership.Denied
}
func (n namespacesAccess) Namespaces() ([]string, bool) { return n, false }

// userGraph is the graph relationshipTopologyForUser gives a user who may
// read namespaces and every cluster-scoped kind: a copy without the nodes in
// other namespaces.
func userGraph(shared *topology.Topology, namespaces []string) *topology.Topology {
	copied := *shared
	copied.Nodes = append([]topology.Node(nil), shared.Nodes...)
	copied.Edges = append([]topology.Edge(nil), shared.Edges...)
	denied := map[string]bool{}
	for _, n := range copied.Nodes {
		if ns, _ := n.Data["namespace"].(string); ns != "" && !slices.Contains(namespaces, ns) {
			denied[n.ID] = true
		}
	}
	copied.StripNodeIDs(denied)
	return &copied
}

// withholdNamespaces is withholdDirectLookups for that user: the direct
// reads' Pods and claims outside their namespaces go.
func withholdNamespaces(rel *topology.Relationships, namespaces []string) {
	rel.Pods = slices.DeleteFunc(rel.Pods, func(p topology.ResourceRef) bool { return !slices.Contains(namespaces, p.Namespace) })
	rel.Consumers = slices.DeleteFunc(rel.Consumers, func(c topology.ResourceRef) bool {
		return c.Kind == "PersistentVolumeClaim" && c.Group == "" && !slices.Contains(namespaces, c.Namespace)
	})
}

func main() {
	contextName := flag.String("context", "", "kubeconfig context to read")
	out := flag.String("out", "", "write the JSON report here")
	perKind := flag.Int("max-per-kind", 0, "answer at most this many objects of each kind (0: all)")
	only := flag.String("only", "", "answer only objects of these kinds, comma-separated (Kind or Kind.group)")
	cpuprofile := flag.String("cpuprofile", "", "write a CPU profile of the answers (both ways) here")
	explain := flag.String("explain", "", "print both answers for the subject with this resource key (group|Kind|namespace|name)")
	flag.Parse()
	if *contextName == "" {
		log.Fatal("--context is required")
	}
	log.SetFlags(log.Ltime)
	rep := report{Context: *contextName, Started: time.Now(), Kinds: map[string]int{}, Timings: map[string]*timing{}, FamilyGaps: map[string]int{}, RestrictedTimings: map[string]*timing{}, ManagedBy: map[string]int{}, Examples: map[string][]string{}}

	kubeconfig := cmp.Or(os.Getenv("KUBECONFIG"), filepath.Join(homedir.HomeDir(), ".kube", "config"))
	if err := k8s.Initialize(k8s.InitOptions{KubeconfigPath: kubeconfig, PreferredContext: k8s.ContextRef{Name: *contextName, SourceFile: kubeconfig, InFileName: *contextName}}); err != nil {
		log.Fatalf("initialize: %v", err)
	}
	if got := k8s.GetContextName(); got != *contextName {
		log.Fatalf("connected to %q, not %q", got, *contextName)
	}
	ctx := context.Background()
	if err := k8s.InitAllSubsystems(ctx, func(msg string) { log.Print(msg) }); err != nil {
		log.Fatalf("init subsystems: %v", err)
	}
	cache := k8s.GetResourceCache()
	select {
	case <-cache.DeferredDone():
	case <-time.After(5 * time.Minute):
		log.Fatal("deferred informers didn't sync in 5 minutes")
	}
	dc, rd := k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()
	resources, err := rd.GetAPIResources()
	if err != nil {
		log.Fatalf("discovery: %v", err)
	}
	var crds []schema.GroupVersionResource
	crdKind := map[schema.GroupVersionResource]string{}
	for _, r := range resources {
		if !r.IsCRD || !slices.Contains(r.Verbs, "list") || !slices.Contains(r.Verbs, "watch") {
			continue
		}
		gvr := schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Name}
		if err := dc.EnsureWatching(gvr); err != nil {
			log.Printf("watch %v: %v", gvr, err)
			continue
		}
		crds = append(crds, gvr)
		crdKind[gvr] = r.Kind
	}
	for _, gvr := range crds {
		if !dc.WaitForSync(gvr, 2*time.Minute) {
			log.Printf("%v didn't sync; its objects are left out", gvr)
		}
	}
	rep.WatchedCRDs = len(crds)
	log.Printf("caches synced; %d CRD kinds watched", len(crds))

	provider := k8s.NewTopologyResourceProvider(cache)
	dp := k8s.NewTopologyDynamicProvider(dc, rd)
	opts := topology.RelationshipBuildOptions()
	opts.ViewMode = topology.ViewModeResources
	opts.IncludeSecrets = true

	heap := func() uint64 {
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	base := heap()
	rep.CacheHeapMiB = float64(base) / (1 << 20)
	var topo *topology.Topology
	for range 3 {
		start := time.Now()
		topo, err = topology.NewBuilder(provider).WithDynamic(dp).Build(opts)
		if err != nil {
			log.Fatalf("build graph: %v", err)
		}
		rep.GraphBuildMS = append(rep.GraphBuildMS, float64(time.Since(start).Microseconds())/1000)
	}
	start := time.Now()
	index := topology.IndexByResource(topo)
	rep.GraphIndexMS = float64(time.Since(start).Microseconds()) / 1000
	rep.GraphHeldMiB = float64(int64(heap())-int64(base)) / (1 << 20)
	rep.GraphNodes, rep.GraphEdges = len(topo.Nodes), len(topo.Edges)
	log.Printf("graph: %d nodes, %d edges, built in %v ms, holds %.1f MiB", rep.GraphNodes, rep.GraphEdges, rep.GraphBuildMS, rep.GraphHeldMiB)

	subjects := collect(provider, dc, crds, crdKind, *perKind)
	if *only != "" {
		kinds := strings.Split(*only, ",")
		subjects = slices.DeleteFunc(subjects, func(s subject) bool {
			kind := s.ref.Kind
			if s.ref.Group != "" {
				kind += "." + s.ref.Group
			}
			return !slices.Contains(kinds, kind) && !slices.Contains(kinds, s.ref.Kind)
		})
	}
	rep.Objects = len(subjects)
	istioDomain := ""
	if deployments, err := provider.Deployments(); err == nil {
		istioDomain = configrefs.IstioServiceDomain(deployments)
	}
	unrestricted := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }

	var allNamespaces []string
	if nss, err := cache.Namespaces().List(labels.Everything()); err == nil {
		for _, ns := range nss {
			allNamespaces = append(allNamespaces, ns.Name)
		}
	}
	slices.Sort(allNamespaces)
	var half namespacesAccess
	for i := 0; i < len(allNamespaces); i += 2 {
		half = append(half, allNamespaces[i])
	}
	rep.RestrictedNamespaces = len(half)
	restricted := func(*ownership.Catalog) ownership.Access { return half }
	restrictedDiffs := map[diffKey]*diffGroup{}

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatalf("cpuprofile: %v", err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatalf("cpuprofile: %v", err)
		}
		defer pprof.StopCPUProfile()
	}
	diffs := map[diffKey]*diffGroup{}
	for i, s := range subjects {
		kind := s.ref.Kind
		if s.ref.Group != "" {
			kind += "." + s.ref.Group
		}
		rep.Kinds[kind]++
		t := rep.Timings[kind]
		if t == nil {
			t = &timing{}
			rep.Timings[kind] = t
		}
		t.Count++

		fetched, observed := k8s.OwnershipObservation(s.obj)
		owner, _ := meta.Accessor(s.obj)
		start := time.Now()
		before := topology.GetRelationshipsWithObject(s.ref.Kind, s.ref.Namespace, s.ref.Name, s.obj, topo, provider, dp, index)
		if before == nil {
			before = &topology.Relationships{}
		}
		if observed {
			k8s.AnswerOwnership(before, fetched, owner, unrestricted)
		}
		t.Before = append(t.Before, time.Since(start).Nanoseconds())

		start = time.Now()
		after, coverage := k8s.AnswerRelationships(cache, s.obj, unrestricted, unrestricted, istioDomain)
		t.After = append(t.After, time.Since(start).Nanoseconds())
		if after == nil {
			after = &topology.Relationships{}
		}
		for name, c := range coverage {
			if len(c.NotChecked) > 0 {
				rep.FamilyGaps[name]++
			}
		}
		if *explain != "" && s.ref.Key() == *explain {
			b, _ := json.MarshalIndent(before, "", "  ")
			a, _ := json.MarshalIndent(after, "", "  ")
			fmt.Printf("BEFORE %s\n%s\nAFTER\n%s\n", s.ref.Key(), b, a)
		}
		outcome := managedByOutcome(cache, topo, ownership.Unrestricted(), s, before.ManagedBy, after.ManagedBy)
		if outcome != "" {
			rep.ManagedBy[outcome]++
			if outcome == "lost" {
				rep.ManagedByUnexplained++
			}
			if outcome == "lost" || outcome == "lateral" || len(rep.Examples[outcome]) < 5 {
				rep.Examples[outcome] = append(rep.Examples[outcome], fmt.Sprintf("%s: before %v, after %v", s.ref.Key(), before.ManagedBy, after.ManagedBy))
			}
		}
		if compare(kind, s.ref, before, after, diffs) {
			rep.SubjectsEqual++
		} else {
			rep.SubjectsDiffer++
		}
		if s.ref.Namespace == "" || slices.Contains(half, s.ref.Namespace) {
			t := rep.RestrictedTimings[kind]
			if t == nil {
				t = &timing{}
				rep.RestrictedTimings[kind] = t
			}
			t.Count++
			start := time.Now()
			before := topology.GetRelationshipsWithObject(s.ref.Kind, s.ref.Namespace, s.ref.Name, s.obj, userGraph(topo, half), provider, dp, nil)
			if before == nil {
				before = &topology.Relationships{}
			}
			withholdNamespaces(before, half)
			if observed {
				k8s.AnswerOwnership(before, fetched, owner, restricted)
			}
			t.Before = append(t.Before, time.Since(start).Nanoseconds())
			start = time.Now()
			after, _ := k8s.AnswerRelationships(cache, s.obj, restricted, restricted, istioDomain)
			if after == nil {
				after = &topology.Relationships{}
			}
			t.After = append(t.After, time.Since(start).Nanoseconds())
			if compare(kind, s.ref, before, after, restrictedDiffs) {
				rep.RestrictedEqual++
			} else {
				rep.RestrictedDiffer++
			}
		}
		if (i+1)%2000 == 0 {
			log.Printf("%d/%d answered", i+1, len(subjects))
		}
	}
	for _, ts := range []map[string]*timing{rep.Timings, rep.RestrictedTimings} {
		for _, t := range ts {
			t.BeforeUS, t.AfterUS = percentiles(t.Before), percentiles(t.After)
		}
	}
	for _, g := range restrictedDiffs {
		rep.RestrictedDiffs = append(rep.RestrictedDiffs, *g)
	}
	slices.SortFunc(rep.RestrictedDiffs, func(a, b diffGroup) int { return cmp.Compare(b.Subjects, a.Subjects) })
	for _, g := range diffs {
		rep.Diffs = append(rep.Diffs, *g)
	}
	slices.SortFunc(rep.Diffs, func(a, b diffGroup) int {
		return cmp.Or(cmp.Compare(b.Subjects, a.Subjects), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Field, b.Field))
	})
	data, _ := json.MarshalIndent(rep, "", "  ")
	if *out != "" {
		if err := os.WriteFile(*out, data, 0o644); err != nil {
			log.Fatalf("write report: %v", err)
		}
	}
	fmt.Printf("%s: %d objects, %d equal, %d differ, %d diff groups; restricted user (%d of %d namespaces): %d equal, %d differ, %d groups\n", rep.Context, rep.Objects, rep.SubjectsEqual, rep.SubjectsDiffer, len(rep.Diffs), len(half), len(allNamespaces), rep.RestrictedEqual, rep.RestrictedDiffer, len(rep.RestrictedDiffs))
}

func managedByOutcome(cache *k8s.ResourceCache, graph *topology.Topology, access ownership.Access, subject subject, before, after []topology.ResourceRef) string {
	b, a := refSet(reflect.ValueOf(before)), refSet(reflect.ValueOf(after))
	m, _ := meta.Accessor(subject.obj)
	if b.Equal(a) {
		if ref := topology.ManagedByFromMeta(m); ref != nil && a.Has(ref.Group+"/"+ref.Kind+"/"+ref.Namespace+"/"+ref.Name) {
			return "meta-same"
		}
		return ""
	}
	if len(after) == 0 {
		return "lost"
	}
	if len(before) == 0 {
		return "gained"
	}
	session, err := k8s.BeginOwnershipRead(cache)
	if err != nil {
		log.Fatalf("ManagedBy session: %v", err)
	}

	nodes := map[string]resourceid.Ref{}
	parents := map[resourceid.Ref][]resourceid.Ref{}
	for i := range graph.Nodes {
		node := &graph.Nodes[i]
		if node.Kind == topology.KindPodGroup {
			continue
		}
		nodes[node.ID] = resourceid.ResolveCurrent(topology.NodeReference(node), nil).Ref
	}
	for _, edge := range graph.Edges {
		if edge.Type != topology.EdgeManages {
			continue
		}
		source, hasSource := nodes[edge.Source]
		target, hasTarget := nodes[edge.Target]
		if hasSource && hasTarget {
			parents[target] = append(parents[target], source)
		}
	}
	v := ownership.NewView(session, access)
	ancestor := func(above, below topology.ResourceRef) bool {
		target := resourceid.NewRef(above.Group, above.Kind, above.Namespace, above.Name)
		frontier := []resourceid.Ref{resourceid.NewRef(below.Group, below.Kind, below.Namespace, below.Name)}
		seen := map[resourceid.Ref]bool{}
		for depth := 0; depth < 16 && len(frontier) > 0; depth++ {
			var next []resourceid.Ref
			for _, ref := range frontier {
				if ref == target {
					return true
				}
				if seen[ref] {
					continue
				}
				seen[ref] = true
				if access.CanRead(ref.GroupKind(), ref.Namespace) != ownership.Allowed {
					continue
				}
				for _, parent := range parents[ref] {
					if access.CanRead(parent.GroupKind(), parent.Namespace) == ownership.Allowed {
						next = append(next, parent)
					}
				}
				_, owners := v.References(ref)
				for _, owner := range owners {
					if owner.State == ownership.OwnerFound {
						next = append(next, owner.Ref)
					}
				}
				for _, manager := range v.RecordingManagers(ref).Managers {
					next = append(next, manager.Ref)
				}
			}
			frontier = next
		}
		return false
	}
	related := false
	for _, b := range before {
		for _, a := range after {
			related = related || ancestor(a, b) || ancestor(b, a)
		}
	}
	if err := session.ValidateCaches(); err != nil {
		log.Fatalf("ManagedBy caches: %v", err)
	}
	if related {
		return "reached-top"
	}
	return "lateral"
}

// percentiles are the p50, p95 and max of ns, in microseconds.
func percentiles(ns []int64) []float64 {
	if len(ns) == 0 {
		return nil
	}
	s := slices.Clone(ns)
	slices.Sort(s)
	at := func(p float64) float64 { return float64(s[min(len(s)-1, int(p*float64(len(s))))]) / 1000 }
	return []float64{at(0.5), at(0.95), float64(s[len(s)-1]) / 1000}
}

// compare records every field where before and after name different objects
// and reports whether they agree.
func compare(kind string, subject resourceid.Ref, before, after *topology.Relationships, diffs map[diffKey]*diffGroup) bool {
	equal := true
	bv, av := reflect.ValueOf(before).Elem(), reflect.ValueOf(after).Elem()
	rt := bv.Type()
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() || f.Tag.Get("json") == "-" || f.Name == "Ownership" {
			continue
		}
		b, a := refSet(bv.Field(i)), refSet(av.Field(i))
		if b.Equal(a) {
			continue
		}
		equal = false
		for side, only := range map[string]sets.Set[string]{"before": b.Difference(a), "after": a.Difference(b)} {
			byKind := map[string][]string{}
			for key := range only {
				byKind[refKindOf(key)] = append(byKind[refKindOf(key)], key)
			}
			for refKind, keys := range byKind {
				k := diffKey{kind, f.Name, side, refKind}
				g := diffs[k]
				if g == nil {
					g = &diffGroup{Kind: kind, Field: f.Name, Side: side, RefKind: refKind}
					diffs[k] = g
				}
				g.Subjects++
				g.Refs += len(keys)
				if len(g.Examples) < 5 {
					slices.Sort(keys)
					g.Examples = append(g.Examples, subject.Key()+" -> "+keys[0])
				}
			}
		}
	}
	return equal
}

func refKindOf(key string) string {
	parts := strings.Split(key, "/")
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return key
}

// refSet is the identities a Relationships field names: a ref, a list of
// refs, or the Reflection's source and mirrors.
func refSet(v reflect.Value) sets.Set[string] {
	out := sets.New[string]()
	add := func(r topology.ResourceRef) { out.Insert(r.Group + "/" + r.Kind + "/" + r.Namespace + "/" + r.Name) }
	switch x := v.Interface().(type) {
	case *topology.ResourceRef:
		if x != nil {
			add(*x)
		}
	case []topology.ResourceRef:
		for _, r := range x {
			add(r)
		}
	case *topology.ReflectionRelationships:
		if x != nil {
			if x.Source != nil {
				out.Insert("source:" + x.Source.Group + "/" + x.Source.Kind + "/" + x.Source.Namespace + "/" + x.Source.Name)
			}
			if x.SourceResourceVersion != "" {
				out.Insert("sourceResourceVersion:" + x.SourceResourceVersion)
			}
			for _, m := range x.Mirrors {
				out.Insert("mirror:" + m.Group + "/" + m.Kind + "/" + m.Namespace + "/" + m.Name)
			}
		}
	case *topology.ControllerChain:
		if x != nil {
			for _, r := range x.Refs {
				add(r)
			}
			out.Insert("stop:" + x.Stop)
			out.Insert("reason:" + x.Reason)
		}
	default:
		data, err := json.Marshal(v.Interface())
		if err != nil {
			panic(err)
		}
		out.Insert("value:" + string(data))
	}
	return out
}

// collect lists every cached object: the typed kinds the provider serves and
// every watched CRD kind.
func collect(p topology.ResourceProvider, dc *k8s.DynamicResourceCache, crds []schema.GroupVersionResource, crdKind map[schema.GroupVersionResource]string, perKind int) []subject {
	var out []subject
	add := func(group, kind string, objs []any) {
		if perKind > 0 && len(objs) > perKind {
			objs = objs[:perKind]
		}
		for _, o := range objs {
			cached, isObject := o.(k8sruntime.Object)
			if !isObject {
				continue
			}
			// As the server fetches it: a copy with its kind set, so the
			// informer's own object is never touched.
			obj := cached.DeepCopyObject()
			if obj.GetObjectKind().GroupVersionKind().Kind == "" {
				version := "v1"
				obj.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{Group: group, Version: version, Kind: kind})
			}
			m, err := meta.Accessor(obj)
			if err != nil {
				continue
			}
			out = append(out, subject{ref: resourceid.NewRef(group, kind, m.GetNamespace(), m.GetName()), obj: obj})
		}
	}
	anys := func(list any, err error) []any {
		if err != nil {
			return nil
		}
		v := reflect.ValueOf(list)
		res := make([]any, v.Len())
		for i := range v.Len() {
			res[i] = v.Index(i).Interface()
		}
		return res
	}
	add("", "Pod", anys(p.Pods()))
	add("", "Service", anys(p.Services()))
	add("apps", "Deployment", anys(p.Deployments()))
	add("apps", "DaemonSet", anys(p.DaemonSets()))
	add("apps", "StatefulSet", anys(p.StatefulSets()))
	add("apps", "ReplicaSet", anys(p.ReplicaSets()))
	add("batch", "Job", anys(p.Jobs()))
	add("batch", "CronJob", anys(p.CronJobs()))
	add("networking.k8s.io", "Ingress", anys(p.Ingresses()))
	add("", "ConfigMap", anys(p.ConfigMaps()))
	add("", "Secret", anys(p.Secrets()))
	add("", "PersistentVolumeClaim", anys(p.PersistentVolumeClaims()))
	add("", "PersistentVolume", anys(p.PersistentVolumes()))
	add("autoscaling", "HorizontalPodAutoscaler", anys(p.HorizontalPodAutoscalers()))
	add("policy", "PodDisruptionBudget", anys(p.PodDisruptionBudgets()))
	add("networking.k8s.io", "NetworkPolicy", anys(p.NetworkPolicies()))
	add("", "Node", anys(p.Nodes()))
	if sa, ok := p.(topology.ServiceAccountProvider); ok {
		add("", "ServiceAccount", anys(sa.ServiceAccounts()))
	}
	for _, gvr := range crds {
		objs, err := dc.List(gvr, "")
		if err != nil {
			continue
		}
		add(gvr.Group, crdKind[gvr], anys(objs, nil))
	}
	return out
}
