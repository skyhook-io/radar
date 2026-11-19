package server

import (
	"context"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// appRelationBatch is request-local and policy-neutral. Each family is read
// once; rows only join the resulting target-to-declarer maps.
type appRelationBatch struct {
	fields map[string]map[resourceid.Ref][]topology.ResourceRef
	// services is where Services went unread. A Service selects only in its
	// own namespace, so a workload's exposure is unknown only when its own
	// namespace's Services went unread.
	services appUnread
	// selectable is where each kind a Service can select went unread: a
	// workload the batch didn't read, though its row's own read found it, had
	// no selection decided.
	selectable map[resourceid.GroupKind]*appUnread
	// exposureUndecided are the objects whose Service selection couldn't be
	// decided: a lookup their candidate or a selector needed couldn't be
	// read, or the selector couldn't be evaluated against them.
	exposureUndecided map[resourceid.Ref]bool
	// loading: a kind the attachments join was still loading.
	loading bool
}

// appStillLoading reports whether any gap is a kind still loading: rows built
// over it are answered but not cached, so the next request regroups them.
func appStillLoading(gaps ...ownership.Gap) bool {
	return slices.ContainsFunc(gaps, func(g ownership.Gap) bool { return g.Reason == readreason.KindSyncPending })
}

// appUnread is where reads of one kind left its objects unread.
type appUnread struct {
	everywhere bool
	namespaces map[string]bool
	// others answers for a namespace the read didn't name: a collector
	// holding only some namespaces reports all the others as one gap.
	others func(namespace string) bool
}

func (u *appUnread) note(session relations.Session, kind resourceid.GroupKind, gaps []ownership.Gap) {
	for _, g := range gaps {
		switch {
		case g.Namespace != "":
			if u.namespaces == nil {
				u.namespaces = map[string]bool{}
			}
			u.namespaces[g.Namespace] = true
		case g.Reason == readreason.RadarNamespaceNotCollected:
			if u.others != nil {
				continue
			}
			unread := map[string]bool{}
			u.others = func(namespace string) bool {
				if got, ok := unread[namespace]; ok {
					return got
				}
				read := session.Indexed(kind, "", nil, ownership.SearchScope{Namespaces: []string{namespace}})
				unread[namespace] = len(read.Gaps) > 0
				return unread[namespace]
			}
		default:
			u.everywhere = true
		}
	}
}

func (u *appUnread) in(namespace string) bool {
	return u != nil && (u.everywhere || u.namespaces[namespace] || u.others != nil && u.others(namespace))
}

func (b *appRelationBatch) undecided(ref resourceid.Ref) {
	if b.exposureUndecided == nil {
		b.exposureUndecided = map[resourceid.Ref]bool{}
	}
	b.exposureUndecided[ref] = true
}

func appAttachmentField(field string) bool {
	return slices.Contains([]string{"Services", "Ingresses", "Routes", "Gateways", "Backends", "ConfigRefs", "Scalers", "StorageRefs", "PDBs", "NetworkPolicies"}, field)
}

func appNeedsDisposition(d relations.Disposition) bool {
	for _, rule := range d.Rules {
		for _, field := range slices.Concat(rule.ReferenceFields, rule.ReferrerFields) {
			if appAttachmentField(field) {
				return true
			}
		}
	}
	return false
}

// appBatchField is the field an entry projected onto a subject from other
// fills, and false when the batch drops it: Gateways and Backends count only
// from routes, as Routes.
func appBatchField(field string, other resourceid.GroupKind) (string, bool) {
	if !appAttachmentField(field) {
		return "", false
	}
	if field == "Gateways" || field == "Backends" {
		return "Routes", isAppRouteKind(other.Kind)
	}
	return field, true
}

var appServiceKind = resourceid.GroupKind{Kind: "Service"}

// appFieldRead reports whether rows read field on an object of kind subject:
// a workload's own fields, and the Ingresses and routes of a Service, which
// linkEntrypoints carries to the workloads it selects.
func appFieldRead(field string, subject resourceid.GroupKind) bool {
	return slices.Contains(appWorkloadKinds, subject) || subject == appServiceKind && (field == "Ingresses" || field == "Routes")
}

// appProjectionRead reports whether d, projecting a declaration of a target
// kind by a source kind, fills a field rows read.
func appProjectionRead(d relations.Disposition, source, target resourceid.GroupKind) bool {
	for _, rule := range d.Rules {
		if !rule.Matches(source, target) {
			continue
		}
		for _, f := range rule.ReferenceFields {
			if field, ok := appBatchField(f, target); ok && appFieldRead(field, source) {
				return true
			}
		}
		for _, f := range rule.ReferrerFields {
			if field, ok := appBatchField(f, source); ok && appFieldRead(field, target) {
				return true
			}
		}
		return false
	}
	return false
}

// appSourceRead reports whether what a source of f declares can fill a field
// rows read. When f's targets are open-ended, a source rows read anything on
// is evaluated, and any other only for a target rows read.
func appSourceRead(f *relations.Family, source relations.Source) bool {
	targets := source.Targets
	if len(targets) == 0 {
		targets = f.Targets
	}
	if f.IsTarget != nil && len(source.Targets) == 0 {
		subjects := append(slices.Clone(appWorkloadKinds), appServiceKind)
		if slices.Contains(subjects, source.Kind) {
			return true
		}
		targets = nil
		for _, gk := range subjects {
			if f.IsTarget(gk) {
				targets = append(targets, gk)
			}
		}
	}
	for _, target := range targets {
		if appProjectionRead(f.Projection, source.Kind, target) {
			return true
		}
	}
	return false
}

// appEvaluation is which sources a batch evaluates, by family, and which
// compositions it joins.
type appEvaluation struct {
	sources      map[*relations.Family]map[resourceid.GroupKind]bool
	compositions []*relations.Composition
}

func (e appEvaluation) evaluate(f *relations.Family, source resourceid.GroupKind) {
	if e.sources[f] == nil {
		e.sources[f] = map[resourceid.GroupKind]bool{}
	}
	e.sources[f][source] = true
}

// appRowEvaluation evaluates only the sources whose declarations can reach a
// row: their own projection fills a field rows read, or a composition joins
// them to an object whose field rows read. Every target stays readable.
func appRowEvaluation(families []*relations.Family, compositions []*relations.Composition) appEvaluation {
	e := appEvaluation{sources: map[*relations.Family]map[resourceid.GroupKind]bool{}}
	for _, f := range families {
		for _, source := range f.Sources {
			if appSourceRead(f, source) {
				e.evaluate(f, source.Kind)
			}
		}
	}
	for _, c := range compositions {
		joined := false
		for _, producer := range c.Producer.Sources {
			for _, family := range c.Consumers {
				for _, consumer := range family.Sources {
					if len(c.ConsumerKinds) > 0 && !slices.Contains(c.ConsumerKinds, consumer.Kind) || !appProjectionRead(c.Projection, producer.Kind, consumer.Kind) {
						continue
					}
					e.evaluate(c.Producer, producer.Kind)
					e.evaluate(family, consumer.Kind)
					joined = true
				}
			}
		}
		if joined {
			e.compositions = append(e.compositions, c)
		}
	}
	return e
}

func (b *appRelationBatch) add(field string, subject, other resourceid.Ref) {
	field, ok := appBatchField(field, other.GroupKind())
	if !ok {
		return
	}
	if b.fields[field] == nil {
		b.fields[field] = map[resourceid.Ref][]topology.ResourceRef{}
	}
	b.fields[field][subject] = append(b.fields[field][subject], topology.ResourceRef{Group: other.Group, Kind: other.Kind, Namespace: other.Namespace, Name: other.Name})
}

func (b *appRelationBatch) project(d relations.Disposition, source, target resourceid.Ref) {
	for _, rule := range d.Rules {
		if !rule.Matches(source.GroupKind(), target.GroupKind()) {
			continue
		}
		for _, field := range rule.ReferenceFields {
			b.add(field, source, target)
		}
		for _, field := range rule.ReferrerFields {
			b.add(field, target, source)
		}
		return
	}
}

func buildAppRelationBatch(ctx context.Context, session relations.Session, namespaces []string) *appRelationBatch {
	return buildPlannedAppRelationBatch(ctx, session, namespaces, appRowEvaluation)
}

func buildPlannedAppRelationBatch(ctx context.Context, session relations.Session, namespaces []string, plan func([]*relations.Family, []*relations.Composition) appEvaluation) *appRelationBatch {
	b := &appRelationBatch{fields: map[string]map[resourceid.Ref][]topology.ResourceRef{}}
	scope := ownership.SearchScope{AllNamespaces: namespaces == nil, Namespaces: namespaces, IncludeClusterScoped: true}
	reads := map[resourceid.GroupKind]relations.IndexedRead{}
	objects := func(kind resourceid.GroupKind) relations.IndexedRead {
		if r, ok := reads[kind]; ok {
			return r
		}
		// A namespaced kind read in named namespaces looks them up in the
		// namespace index rather than scanning a cluster-wide store.
		index, keys := "", []string(nil)
		if namespaced, known := session.Catalog().Scope(kind); known && namespaced && !scope.AllNamespaces {
			index, keys = cache.NamespaceIndex, namespaces
		}
		r := session.Indexed(kind, index, keys, scope)
		reads[kind] = r
		b.loading = b.loading || appStillLoading(r.Gaps...)
		return r
	}
	// lookupFor reads what selecting subject needs; for a Service selection,
	// a read that fails leaves subject's exposure unknown.
	lookup := func(ref resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
		o, r := session.Borrow(ref)
		b.loading = b.loading || r.State == ownership.Unavailable && appStillLoading(r.Gap)
		return o, r
	}
	lookupFor := func(subject resourceid.Ref, exposure bool) relations.Lookup {
		return func(ref resourceid.Ref) (runtime.Object, bool) {
			o, r := lookup(ref)
			if exposure && r.State == ownership.Unavailable {
				b.undecided(subject)
			}
			return o, r.State == ownership.Present
		}
	}
	view := relations.NewView(session, ownership.Unrestricted()).WithLimit(0)
	declarations := map[*relations.Family]map[resourceid.Ref][]resourceid.Ref{}
	// The mesh domain is istiod's, wherever it runs, whatever namespaces the
	// request names.
	deploymentKind := resourceid.GroupKind{Group: "apps", Kind: "Deployment"}
	deployments := objects(deploymentKind)
	if !scope.AllNamespaces {
		deployments = session.Indexed(deploymentKind, "", nil, ownership.SearchScope{AllNamespaces: true})
		b.loading = b.loading || appStillLoading(deployments.Gaps...)
	}
	var controllerDeployments []*appsv1.Deployment
	for _, o := range deployments.Objects {
		if d, ok := o.Object.(*appsv1.Deployment); ok {
			controllerDeployments = append(controllerDeployments, d)
		}
	}
	domain := configrefs.IstioServiceDomain(controllerDeployments)
	families := relations.Families(domain)
	evaluation := plan(families, relations.Compositions())
	for _, f := range families {
		if len(evaluation.sources[f]) == 0 {
			continue
		}
		declarations[f] = map[resourceid.Ref][]resourceid.Ref{}
		for _, source := range f.Sources {
			if !evaluation.sources[f][source.Kind] {
				continue
			}
			for _, o := range objects(source.Kind).Objects {
				if ctx.Err() != nil {
					return b
				}
				refs := view.References(f, o.Ref, o.Object)
				for _, d := range refs.Declared {
					if f == relations.ConfigReferences {
						// Only volume and env references count as an application's configuration.
						allowed := false
						for _, path := range d.Paths {
							if path == "volumes[].configMap.name" || path == "volumes[].secret.secretName" || path == "volumes[].persistentVolumeClaim.claimName" || strings.HasPrefix(path, "containers[].env") || strings.HasPrefix(path, "initContainers[].env") {
								allowed = true
							}
						}
						if !allowed {
							continue
						}
					}
					declarations[f][d.Ref] = append(declarations[f][d.Ref], o.Ref)
					if f == relations.ConfigReferences && (d.Ref.Kind == "Secret" || d.Ref.Kind == "ServiceAccount") {
						continue
					}
					if d.State != relations.TargetPresent {
						b.loading = b.loading || d.State == relations.TargetUnread && appStillLoading(d.Gap)
						continue
					}
					b.project(f.Projection, o.Ref, d.Ref)
				}
			}
		}
	}
	for _, composition := range evaluation.compositions {
		for target, producers := range declarations[composition.Producer] {
			for _, consumerFamily := range composition.Consumers {
				for _, consumer := range declarations[consumerFamily][target] {
					if len(composition.ConsumerKinds) > 0 && !slices.Contains(composition.ConsumerKinds, consumer.GroupKind()) {
						continue
					}
					for _, producer := range producers {
						b.project(composition.Projection, producer, consumer)
					}
				}
			}
		}
	}
	for _, f := range relations.SelectorFamilies() {
		if !appNeedsDisposition(f.Projection) {
			continue
		}
		type labelPair struct{ key, value string }
		type candidateBucket struct {
			all    []relations.Candidate
			labels map[labelPair][]int
		}
		candidates := map[resourceid.GroupKind]map[string]*candidateBucket{}
		for _, selectable := range f.Selectables {
			read := objects(selectable.Kind)
			if f == relations.ServiceSelectors {
				if b.selectable == nil {
					b.selectable = map[resourceid.GroupKind]*appUnread{}
				}
				if b.selectable[selectable.Kind] == nil {
					b.selectable[selectable.Kind] = &appUnread{}
				}
				b.selectable[selectable.Kind].note(session, selectable.Kind, read.Gaps)
			}
			for _, o := range read.Objects {
				c, ok := selectable.Candidate(o.Object, lookupFor(o.Ref, f == relations.ServiceSelectors))
				if ok {
					c.Ref = o.Ref
					byNamespace := candidates[o.Ref.GroupKind()]
					if byNamespace == nil {
						byNamespace = map[string]*candidateBucket{}
						candidates[o.Ref.GroupKind()] = byNamespace
					}
					bucket := byNamespace[o.Ref.Namespace]
					if bucket == nil {
						bucket = &candidateBucket{}
						if f == relations.ServiceSelectors {
							bucket.labels = map[labelPair][]int{}
						}
						byNamespace[o.Ref.Namespace] = bucket
					}
					bucket.all = append(bucket.all, c)
					if bucket.labels != nil {
						for key, value := range c.Labels {
							pair := labelPair{key, value}
							bucket.labels[pair] = append(bucket.labels[pair], len(bucket.all)-1)
						}
					}
				}
			}
		}
		seen := map[resourceid.Ref]bool{}
		for _, selector := range f.Selectors {
			read := objects(selector.Kind)
			if f == relations.ServiceSelectors {
				b.services.note(session, selector.Kind, read.Gaps)
			}
			for _, o := range read.Objects {
				if ctx.Err() != nil {
					return b
				}
				id := o.Ref
				if f.Identity != nil {
					id = f.Identity(id)
				}
				if seen[id] {
					continue
				}
				seen[id] = true
				selection, ok, err := topology.ApplicationSelection(selector, o.Object)
				if err != nil || !ok {
					continue
				}
				for _, selectable := range f.Selectables {
					if len(selector.Selects) > 0 && !slices.Contains(selector.Selects, selectable.Kind) {
						continue
					}
					byNamespace := candidates[selectable.Kind]
					matchBucket := func(bucket *candidateBucket) {
						if bucket == nil {
							return
						}
						project := func(c relations.Candidate) {
							exposure := f == relations.ServiceSelectors
							yes, decided := selection.Matches(c, lookupFor(c.Ref, exposure))
							if exposure && !decided {
								b.undecided(c.Ref)
							}
							if yes && decided {
								b.project(f.Projection, o.Ref, c.Ref)
							}
						}
						if f == relations.ServiceSelectors {
							service := o.Object.(*corev1.Service)
							var matching []int
							first := true
							for key, value := range service.Spec.Selector {
								subset := bucket.labels[labelPair{key, value}]
								if first || len(subset) < len(matching) {
									matching, first = subset, false
								}
							}
							for _, index := range matching {
								project(bucket.all[index])
							}
							return
						}
						for _, c := range bucket.all {
							project(c)
						}
					}
					if selection.AnyNamespace {
						for _, bucket := range byNamespace {
							matchBucket(bucket)
						}
					} else {
						namespaces := selection.Namespaces
						if len(namespaces) == 0 {
							namespaces = []string{o.Ref.Namespace}
						}
						for i, namespace := range namespaces {
							if !slices.Contains(namespaces[:i], namespace) {
								matchBucket(byNamespace[namespace])
							}
						}
					}
				}
			}
		}
	}
	b.linkEntrypoints()
	return b
}

func (b *appRelationBatch) relationshipsForRef(group, kind, ns, name string) *appRelationships {
	subject := resourceid.NewRef(group, kind, ns, name)
	field := func(name string) map[string]topology.ResourceRef { return refsByKey(b.fields[name][subject]) }
	out := &appRelationships{serviceRefs: field("Services"), ingressRefs: field("Ingresses"), routeRefs: field("Routes"), configRefs: field("ConfigRefs"), scalerRefs: field("Scalers"), storageRefs: field("StorageRefs"), pdbRefs: field("PDBs"), networkPolicyRefs: field("NetworkPolicies"), exposureIncomplete: b.services.in(ns) || b.selectable[subject.GroupKind()].in(ns) || b.exposureUndecided[subject]}
	out.Configs, out.Scalers, out.Storage, out.PDBs, out.NetworkPolicies = len(out.configRefs), len(out.scalerRefs), len(out.storageRefs), len(out.pdbRefs), len(out.networkPolicyRefs)
	out.Services = refNames(sortedRefs(out.serviceRefs, 20), 20)
	out.Ingresses = refNames(sortedRefs(out.ingressRefs, 20), 20)
	out.Routes = routeRefNames(sortedRefs(out.routeRefs, 20), 20)
	if len(out.serviceRefs)+len(out.ingressRefs)+len(out.routeRefs)+out.Configs+out.Scalers+out.Storage+out.PDBs+out.NetworkPolicies == 0 && !out.exposureIncomplete {
		return nil
	}
	return out
}

// applicationOwnerAbove is topology.OwnerAbove, except that the controller
// chain ends at the first GitOps manager on it: past one lies a parent such
// as an ApplicationSet, shared by apps that are separate.
func applicationOwnerAbove(view *ownership.View, r resourceid.Ref) (resourceid.Ref, bool) {
	if chain := view.ControllerChain(r).Chain; len(chain) > 1 {
		for _, owner := range chain[1:] {
			if isGitOpsManagerKind(topology.NodeKind(owner.Kind), owner.Group) {
				return owner, true
			}
		}
		return chain[len(chain)-1], true
	}
	return topology.OwnerAbove(view, r)
}

// applicationStructuralRoot is the object start's application is rooted at,
// and whether a manager kind it read was still loading, which can move it.
func applicationStructuralRoot(session relations.Session, view *ownership.View, start resourceid.Ref) (resourceid.Ref, bool) {
	cur := start
	loading := false
	seen := map[resourceid.Ref]bool{}
	for !seen[cur] {
		seen[cur] = true
		if isGitOpsManagerKind(topology.NodeKind(cur.Kind), cur.Group) {
			return cur, loading
		}
		if next, ok := applicationOwnerAbove(view, cur); ok && !seen[next] {
			cur = next
			continue
		}
		if session != nil {
			answer := relations.NewView(session, ownership.Unrestricted()).References(relations.FluxHelmOrigins, cur, nil)
			for _, d := range answer.Declared {
				obj, read := session.Borrow(d.Ref)
				loading = loading || d.State == relations.TargetUnread && appStillLoading(d.Gap) || read.State == ownership.Unavailable && appStillLoading(read.Gap)
				if u, ok := obj.(*unstructured.Unstructured); ok && d.State == relations.TargetPresent && read.State == ownership.Present && gitops.FluxTargetsLocalCluster(u) && !gitops.FluxMembersReadable(u) {
					return d.Ref, loading
				}
			}
		}
		recording := view.RecordingManagers(cur)
		loading = loading || appStillLoading(recording.Gaps...)
		// Inventory edges are emitted Argo, Kustomization, then HelmRelease.
		for _, gk := range []resourceid.GroupKind{{Group: "argoproj.io", Kind: "Application"}, {Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization"}, {Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease"}} {
			var refs []resourceid.Ref
			for _, m := range recording.Managers {
				if m.Ref.GroupKind() == gk {
					refs = append(refs, m.Ref)
				}
			}
			if len(refs) > 0 {
				return pickRecordingManager(session, cur, refs), loading
			}
		}

		break
	}
	return cur, loading
}

// pickRecordingManager chooses among managers of one kind that all record
// subject. The manager subject's own metadata names (an Argo CD tracking ID or
// instance label, Flux labels) wins, since it applied the object last;
// otherwise the first by key, so the choice doesn't depend on read order.
func pickRecordingManager(session relations.Session, subject resourceid.Ref, refs []resourceid.Ref) resourceid.Ref {
	slices.SortFunc(refs, func(a, b resourceid.Ref) int { return strings.Compare(a.Key(), b.Key()) })
	if len(refs) == 1 || session == nil {
		return refs[0]
	}
	read := session.Object(subject)
	if read.State != ownership.Present {
		return refs[0]
	}
	for _, c := range read.Object.Claims {
		if c.Copied {
			continue
		}
		for _, r := range refs {
			if c.Manager.GroupKind() == r.GroupKind() && c.Manager.Name == r.Name && (c.Manager.Namespace == "" || c.Manager.Namespace == r.Namespace) {
				return r
			}
		}
	}
	return refs[0]
}

type applicationInventory struct {
	batch   *appRelationBatch
	session relations.Session
	owners  *ownership.View
	// loading: a manager kind a root was resolved from was still loading.
	loading bool
}

// cacheable reports whether every manager and attachment kind the rows were
// joined from had loaded.
func (a *applicationInventory) cacheable() bool {
	return !a.loading && !a.batch.loading
}

func (a *applicationInventory) rootOf(group, kind, ns, name string) (string, string, string, string) {
	r, loading := applicationStructuralRoot(a.session, a.owners, resourceid.NewRef(group, kind, ns, name))
	a.loading = a.loading || loading
	displayKind := topology.NodeKindForGVK(r.Kind, r.Group)
	return r.Namespace + "/" + displayKind + "/" + r.Name, displayKind, r.Group, r.Key()
}
func (a *applicationInventory) relationshipsFor(kind, ns, name string, obj any) *appRelationships {
	group := appWorkloadAPIGroup(kind)
	if u, ok := obj.(*unstructured.Unstructured); ok {
		group = resourceid.GroupFromAPIVersion(u.GetAPIVersion())
	}
	return a.batch.relationshipsForRef(group, kind, ns, name)
}

func (a *applicationInventory) resolveEventGroup(kind, ns, name string) (string, bool) {
	group, found := resourceid.GroupForBuiltinKind(kind), false
	for _, candidate := range a.session.Catalog().GroupsForKind(kind) {
		r := resourceid.NewRef(candidate, kind, ns, name)
		namespaced, _ := a.session.Catalog().Scope(r.GroupKind())
		if !namespaced {
			r.Namespace = ""
		}
		if a.session.Object(r).State != ownership.Present {
			continue
		}
		if found && group != r.Group {
			return "", false
		}
		group, found = r.Group, true
	}
	return group, true
}

func (b *appRelationBatch) linkEntrypoints() {
	// A workload's Service exposes the Service's incoming routes and Ingresses.
	for workload, services := range b.fields["Services"] {
		for _, service := range services {
			ref := resourceid.NewRef(service.Group, service.Kind, service.Namespace, service.Name)
			for _, field := range []string{"Ingresses", "Routes"} {
				if b.fields[field] == nil {
					b.fields[field] = map[resourceid.Ref][]topology.ResourceRef{}
				}
				b.fields[field][workload] = append(b.fields[field][workload], b.fields[field][ref]...)
			}
		}
	}
}
