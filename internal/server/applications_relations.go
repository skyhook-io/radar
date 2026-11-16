package server

import (
	"context"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

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
	// exposureIncomplete: Services went unread everywhere; exposureGaps names
	// the namespaces they went unread in. A Service selects only in its own
	// namespace, so a workload's exposure is unknown only when its own
	// namespace's Services went unread.
	exposureIncomplete bool
	exposureGaps       map[string]bool
	// exposureUnread answers for a namespace the Services read didn't name: a
	// collector holding only some namespaces reports all the others as one gap.
	exposureUnread func(namespace string) bool
}

func (b *appRelationBatch) exposureUnknown(namespace string) bool {
	return b.exposureIncomplete || b.exposureGaps[namespace] || b.exposureUnread != nil && b.exposureUnread(namespace)
}

func (b *appRelationBatch) noteServiceGaps(session relations.Session, kind resourceid.GroupKind, gaps []ownership.Gap) {
	for _, g := range gaps {
		switch {
		case g.Namespace != "":
			if b.exposureGaps == nil {
				b.exposureGaps = map[string]bool{}
			}
			b.exposureGaps[g.Namespace] = true
		case g.Reason == readreason.RadarNamespaceNotCollected:
			if b.exposureUnread != nil {
				continue
			}
			unread := map[string]bool{}
			b.exposureUnread = func(namespace string) bool {
				if got, ok := unread[namespace]; ok {
					return got
				}
				read := session.Indexed(kind, "", nil, ownership.SearchScope{Namespaces: []string{namespace}})
				unread[namespace] = len(read.Gaps) > 0
				return unread[namespace]
			}
		default:
			b.exposureIncomplete = true
		}
	}
}

func appAttachmentField(field string) bool {
	return slices.Contains([]string{"Services", "Ingresses", "Routes", "Gateways", "Backends", "ConfigRefs", "Scalers", "StorageRefs", "PDBs", "NetworkPolicies"}, field)
}

func appNeedsDisposition(d relations.Disposition) bool {
	for _, rule := range d.Rules {
		for _, field := range slices.Concat(rule.References, rule.Referrers) {
			if appAttachmentField(field) {
				return true
			}
		}
	}
	return false
}

func (b *appRelationBatch) add(field string, subject, other resourceid.Ref) {
	if !appAttachmentField(field) {
		return
	}
	if (field == "Gateways" || field == "Backends") && !isAppRouteKind(other.Kind) {
		return
	}
	if field == "Gateways" || field == "Backends" {
		field = "Routes"
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
		for _, field := range rule.References {
			b.add(field, source, target)
		}
		for _, field := range rule.Referrers {
			b.add(field, target, source)
		}
		return
	}
}

func buildAppRelationBatch(ctx context.Context, session relations.Session, namespaces []string) *appRelationBatch {
	b := &appRelationBatch{fields: map[string]map[resourceid.Ref][]topology.ResourceRef{}}
	scope := ownership.SearchScope{AllNamespaces: namespaces == nil, Namespaces: namespaces, IncludeClusterScoped: true}
	reads := map[resourceid.GroupKind]relations.IndexedRead{}
	objects := func(kind resourceid.GroupKind) relations.IndexedRead {
		if r, ok := reads[kind]; ok {
			return r
		}
		r := session.Indexed(kind, "", nil, scope)
		reads[kind] = r
		return r
	}
	lookup := func(ref resourceid.Ref) (runtime.Object, bool) {
		o, r := session.Borrow(ref)
		return o, r.State == ownership.Present
	}
	view := relations.NewView(session, ownership.Unrestricted()).WithLimit(0)
	declarations := map[*relations.Family]map[resourceid.Ref][]resourceid.Ref{}
	compositions := relations.Compositions()
	deployments := objects(resourceid.GroupKind{Group: "apps", Kind: "Deployment"})
	var controllerDeployments []*appsv1.Deployment
	for _, o := range deployments.Objects {
		if d, ok := o.Object.(*appsv1.Deployment); ok {
			controllerDeployments = append(controllerDeployments, d)
		}
	}
	domain := configrefs.IstioServiceDomain(controllerDeployments)
	for _, f := range relations.Families(domain) {
		needed := appNeedsDisposition(f.Projection)
		for _, composition := range compositions {
			needed = needed || f == composition.Producer || slices.Contains(composition.Consumers, f)
		}
		if !needed {
			continue
		}
		declarations[f] = map[resourceid.Ref][]resourceid.Ref{}
		for _, source := range f.Sources {
			for _, o := range objects(source.Kind).Objects {
				if ctx.Err() != nil {
					b.exposureIncomplete = true
					return b
				}
				refs := view.References(f, o.Ref, o.Object)
				for _, d := range refs.Declared {
					if f == relations.ConfigReferences {
						// Applications retains the display graph's narrower config surface.
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
					if d.State != relations.TargetPresent || f == relations.ConfigReferences && (d.Ref.Kind == "Secret" || d.Ref.Kind == "ServiceAccount") {
						continue
					}
					b.project(f.Projection, o.Ref, d.Ref)
				}
			}
		}
	}
	for _, composition := range compositions {
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
			for _, o := range objects(selectable.Kind).Objects {
				c, ok := selectable.Candidate(o.Object, lookup)
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
				b.noteServiceGaps(session, selector.Kind, read.Gaps)
			}
			for _, o := range read.Objects {
				if ctx.Err() != nil {
					b.exposureIncomplete = true
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
							if yes, _ := selection.Matches(c, lookup); yes {
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
	out := &appRelationships{serviceRefs: field("Services"), ingressRefs: field("Ingresses"), routeRefs: field("Routes"), configRefs: field("ConfigRefs"), scalerRefs: field("Scalers"), storageRefs: field("StorageRefs"), pdbRefs: field("PDBs"), networkPolicyRefs: field("NetworkPolicies"), exposureIncomplete: b.exposureUnknown(ns)}
	out.Configs, out.Scalers, out.Storage, out.PDBs, out.NetworkPolicies = len(out.configRefs), len(out.scalerRefs), len(out.storageRefs), len(out.pdbRefs), len(out.networkPolicyRefs)
	out.Services = refNames(sortedRefs(out.serviceRefs, 20), 20)
	out.Ingresses = refNames(sortedRefs(out.ingressRefs, 20), 20)
	out.Routes = routeRefNames(sortedRefs(out.routeRefs, 20), 20)
	if len(out.serviceRefs)+len(out.ingressRefs)+len(out.routeRefs)+out.Configs+out.Scalers+out.Storage+out.PDBs+out.NetworkPolicies == 0 && !out.exposureIncomplete {
		return nil
	}
	return out
}

func applicationStructuralRoot(session relations.Session, view *ownership.View, start resourceid.Ref) resourceid.Ref {
	cur := start
	seen := map[resourceid.Ref]bool{}
	for len(seen) < 16 && !seen[cur] {
		seen[cur] = true
		if isGitOpsManagerKind(topology.NodeKind(cur.Kind), cur.Group) {
			return cur
		}
		if next, ok := topology.OwnerAbove(view, cur); ok && !seen[next] {
			cur = next
			continue
		}
		if session != nil {
			answer := relations.NewView(session, ownership.Unrestricted()).References(relations.FluxHelmOrigins, cur, nil)
			for _, d := range answer.Declared {
				obj, read := session.Borrow(d.Ref)
				if u, ok := obj.(*unstructured.Unstructured); ok && d.State == relations.TargetPresent && read.State == ownership.Present && gitops.FluxTargetsLocalCluster(u) && !gitops.FluxMembersReadable(u) {
					return d.Ref
				}
			}
		}
		// Inventory edges are emitted Argo, Kustomization, then HelmRelease.
		for _, gk := range []resourceid.GroupKind{{Group: "argoproj.io", Kind: "Application"}, {Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization"}, {Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease"}} {
			var refs []resourceid.Ref
			for _, m := range view.RecordingManagers(cur).Managers {
				if m.Ref.GroupKind() == gk {
					refs = append(refs, m.Ref)
				}
			}
			if len(refs) > 0 {
				return pickRecordingManager(session, cur, refs)
			}
		}

		break
	}
	return cur
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
}

func (a *applicationInventory) rootOf(group, kind, ns, name string) (string, string, string, string) {
	r := applicationStructuralRoot(a.session, a.owners, resourceid.NewRef(group, kind, ns, name))
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
