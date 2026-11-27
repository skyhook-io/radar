package vulnerabilities

import (
	"context"
	"net/url"
	"slices"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/prom"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Ownership says how this answer names who owns a workload. Its presence
// says this Radar reads owners; an older one sends none.
type Ownership struct {
	// TeamLabel is the label key read for a team (always TeamLabel), on the
	// workload, else on its namespace.
	TeamLabel string `json:"teamLabel"`
	// Unavailable is why no owner could be read at all (the caches were
	// still loading or were replaced), as a read code. Owners are then
	// unknown, not absent.
	Unavailable readreason.Code `json:"unavailable,omitempty"`
}

// WorkloadOwner is what Radar knows about who owns one workload the answer
// names. A workload with neither a manager nor a team is not listed: Radar
// found nothing, which doesn't mean nobody owns it.
type WorkloadOwner struct {
	WorkloadRef
	// Manager is the Helm release or GitOps object that deploys it, by the
	// rule list summaries apply to the ownership answer's lead
	// (resourcecontext.ManagerSummary).
	Manager *OwnerManager `json:"manager,omitempty"`
	// ManagerUnknown: no manager could be named, but the evidence doesn't
	// say there is none (it disagrees, names a manager that can't be
	// checked, or Radar couldn't look everywhere).
	ManagerUnknown bool `json:"managerUnknown,omitempty"`
	// Team is the team label's value, and TeamFrom where it was read:
	// "workload" or "namespace".
	Team     string `json:"team,omitempty"`
	TeamFrom string `json:"teamFrom,omitempty"`
}

// OwnerManager is a workload's Helm release, Argo CD Application or Flux
// Kustomization or HelmRelease, and where it deploys from, as far as its
// own object says. It names the app and the repository, never the file to
// change: an image tag can come from CI, an image override or a values
// file Radar doesn't read.
type OwnerManager struct {
	// Tool is helm, argocd or fluxcd.
	Tool      string `json:"tool"`
	Kind      string `json:"kind"`
	Group     string `json:"group,omitempty"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Chart is, for a Helm release, the workload's helm.sh/chart label: the
	// chart whose template rendered it, which for a subchart's workload is
	// the subchart, not the release's chart. Otherwise it is the chart a
	// Flux HelmRelease or an Argo CD source names.
	Chart string `json:"chart,omitempty"`
	// Sources are where the manager deploys from, credentials removed.
	// Empty for a Helm release, which records no source, and when the
	// caller can't read the manager or its source.
	Sources []OwnerSource `json:"sources,omitempty"`
	// Shared: two or more Argo CD or Flux objects list the workload, and
	// this is the one its own metadata names; it is a lead, not the only
	// deployer.
	Shared bool `json:"shared,omitempty"`
}

// OwnerSource is one repository a manager deploys from, and the path in it
// when the manager names one.
type OwnerSource struct {
	Repo string `json:"repo"`
	Path string `json:"path,omitempty"`
	// Chart is the chart this source names, when it is a Helm source of a
	// multi-source Argo CD Application: which source a workload came from
	// is unknown there, so the manager's Chart stays empty.
	Chart string `json:"chart,omitempty"`
}

const helmChartLabel = "helm.sh/chart"

// TeamLabel is the label whose value names a workload's team. Kubernetes
// recommends no team label; "team" is the most common convention.
const TeamLabel = "team"

// resolveOwners names the owners of refs from Radar's caches in one
// ownership session read as the caller (access), with no API call. The
// manager comes from the ownership answer; repositories from the manager's
// own object and, for Flux, its source object, each only where the caller
// may read it. teamLabel is read on the workload, then its namespace.
func resolveOwners(ctx context.Context, cache *k8s.ResourceCache, refs []WorkloadRef, access func(*ownership.Catalog) ownership.Access, teamLabel string) ([]WorkloadOwner, readreason.Code, error) {
	if len(refs) == 0 {
		return nil, "", nil
	}
	session, err := k8s.BeginOwnershipRead(cache)
	if err != nil || len(session.Catalog().DependentKinds()) == 0 {
		return nil, readreason.ClusterConnecting, nil
	}
	rs, ok := session.(relations.Session)
	if !ok {
		return nil, readreason.ReadError, nil
	}
	acc := ownership.Unrestricted()
	if access != nil {
		acc = access(session.Catalog())
	}
	view := ownership.NewView(session, acc)
	r := ownerReader{rs: rs, acc: acc, teamLabel: teamLabel, namespaces: map[string]string{}}
	var out []WorkloadOwner
	for _, w := range refs {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		ns := w.Namespace
		if b, ok := resourceid.BuiltinForKind(w.Kind); ok && b.Group == w.Group && !b.Namespaced {
			// A static Pod's workload is its Node, named with the Pod's
			// namespace; the Node itself has none.
			ns = ""
		}
		ref := resourceid.NewRef(w.Group, w.Kind, ns, w.Name)
		obj, read := rs.Borrow(ref)
		if read.State == ownership.Unavailable || read.State == ownership.Absent {
			// Its store couldn't say, or Pods still name a controller that
			// is gone: unknown, not "nothing found".
			out = append(out, WorkloadOwner{WorkloadRef: w, ManagerUnknown: true})
			continue
		}
		if read.State != ownership.Present || obj == nil {
			continue
		}
		meta, err := apimeta.Accessor(obj)
		if err != nil {
			continue
		}
		owner := WorkloadOwner{WorkloadRef: w}
		owner.Team, owner.TeamFrom = r.team(w.Namespace, meta.GetLabels())
		view.Seed(ownership.Observe(ref, meta))
		answer := view.Manager(ref)
		// The summary rule decides whether the lead may be named; the lead
		// itself keeps its group, which the summary drops for GitOps kinds.
		if resourcecontext.ManagerSummary(answer) != nil {
			owner.Manager = r.manager(*answer.Lead, meta.GetLabels())
			if owner.Manager != nil {
				owner.Manager.Shared = answer.State == ownership.ManagerShared
			}
		}
		owner.ManagerUnknown = owner.Manager == nil && answer.Subject.Present() && answer.State != ownership.ManagerNone
		if owner.Manager != nil || owner.ManagerUnknown || owner.Team != "" {
			out = append(out, owner)
		}
	}
	if session.ValidateCaches() != nil {
		return nil, readreason.ConnectionChanged, nil
	}
	return out, "", nil
}

type ownerReader struct {
	rs        relations.Session
	acc       ownership.Access
	teamLabel string
	// namespaces memoizes each namespace's team label value.
	namespaces map[string]string
}

// object is the cached object at ref when the caller may read it.
func (r *ownerReader) object(ref resourceid.Ref) *unstructured.Unstructured {
	if r.acc.CanRead(ref.GroupKind(), ref.Namespace) != ownership.Allowed {
		return nil
	}
	obj, read := r.rs.Borrow(ref)
	if read.State != ownership.Present {
		return nil
	}
	u, _ := obj.(*unstructured.Unstructured)
	return u
}

func (r *ownerReader) team(namespace string, labels map[string]string) (team, from string) {
	if r.teamLabel == "" {
		return "", ""
	}
	if v := labels[r.teamLabel]; v != "" {
		return v, "workload"
	}
	v, seen := r.namespaces[namespace]
	if !seen {
		ref := resourceid.NewRef("", "Namespace", "", namespace)
		if r.acc.CanRead(ref.GroupKind(), "") == ownership.Allowed {
			if obj, read := r.rs.Borrow(ref); read.State == ownership.Present && obj != nil {
				if meta, err := apimeta.Accessor(obj); err == nil {
					v = meta.GetLabels()[r.teamLabel]
				}
			}
		}
		r.namespaces[namespace] = v
	}
	if v != "" {
		return v, "namespace"
	}
	return "", ""
}

// manager describes the lead. labels are the workload's own.
func (r *ownerReader) manager(ref resourceid.Ref, labels map[string]string) *OwnerManager {
	lead := ref
	m := &OwnerManager{Kind: lead.Kind, Group: lead.Group, Namespace: lead.Namespace, Name: lead.Name}
	switch {
	case lead.Kind == "HelmRelease" && lead.Group == "":
		m.Tool, m.Chart = "helm", labels[helmChartLabel]
	case lead.Kind == "Application" && lead.Group == "argoproj.io":
		m.Tool = "argocd"
		if app := r.object(ref); app != nil {
			m.Sources, m.Chart = argoSources(app)
		}
	case lead.Kind == "Kustomization" && lead.Group == "kustomize.toolkit.fluxcd.io", lead.Kind == "HelmRelease" && lead.Group == "helm.toolkit.fluxcd.io":
		m.Tool = "fluxcd"
		if root := r.object(ref); root != nil {
			m.Sources, m.Chart = r.fluxSources(root)
		}
	default:
		return nil
	}
	return m
}

// argoSources are an Application's repositories: its hydrator's dry source
// (where people edit) when it has one, else its sources, else its source,
// the precedence Argo CD applies when both are set. chart is the first
// source's that names one.
func argoSources(app *unstructured.Unstructured) (sources []OwnerSource, chart string) {
	// declared counts every source, kept or not: a withheld address still
	// makes the Application multi-source.
	declared := 0
	add := func(raw any) {
		declared++
		src, _ := raw.(map[string]any)
		repo := safeRepo(gitops.StringValue(src["repoURL"]))
		if repo == "" {
			return
		}
		sources = append(sources, OwnerSource{Repo: repo, Path: gitops.StringValue(src["path"]), Chart: gitops.StringValue(src["chart"])})
	}
	// One source's chart is the workload's; with several, which one made
	// the workload is unknown, so each source keeps its own.
	single := func() ([]OwnerSource, string) {
		if declared == 1 && len(sources) == 1 {
			chart, sources[0].Chart = sources[0].Chart, ""
		}
		return sources, chart
	}
	if dry, _, _ := unstructured.NestedFieldNoCopy(app.Object, "spec", "sourceHydrator", "drySource"); dry != nil {
		add(dry)
		return single()
	}
	if list, _, _ := unstructured.NestedFieldNoCopy(app.Object, "spec", "sources"); list != nil {
		if items, _ := list.([]any); len(items) > 0 {
			for _, src := range items {
				add(src)
			}
			return single()
		}
	}
	if src, _, _ := unstructured.NestedFieldNoCopy(app.Object, "spec", "source"); src != nil {
		add(src)
	}
	return single()
}

// fluxSources follow a Kustomization's or HelmRelease's source reference to
// the source object's URL, through a HelmChart when the release names one.
func (r *ownerReader) fluxSources(root *unstructured.Unstructured) (sources []OwnerSource, chart string) {
	path, _, _ := unstructured.NestedString(root.Object, "spec", "path")
	if root.GetKind() == "HelmRelease" {
		chart, _, _ = unstructured.NestedString(root.Object, "spec", "chart", "spec", "chart")
	}
	obj := root
	for hop := 0; hop < 2; hop++ {
		refs := gitops.FluxSourceReferences(obj)
		if len(refs) == 0 {
			return nil, chart
		}
		src := r.object(refs[0].Ref)
		if src == nil {
			return nil, chart
		}
		if src.GetKind() == "HelmChart" {
			if c, _, _ := unstructured.NestedString(src.Object, "spec", "chart"); c != "" {
				chart = c
			}
			obj = src
			continue
		}
		repo := fluxSourceURL(src)
		if repo == "" {
			return nil, chart
		}
		// A chart read from a Git repository or bucket is a path in it.
		if (src.GetKind() == "GitRepository" || src.GetKind() == "Bucket") && path == "" && chart != "" && root.GetKind() == "HelmRelease" {
			path, chart = chart, ""
		}
		safe := safeRepo(repo)
		if safe == "" {
			return nil, chart
		}
		return []OwnerSource{{Repo: safe, Path: path}}, chart
	}
	return nil, chart
}

func fluxSourceURL(src *unstructured.Unstructured) string {
	switch src.GetKind() {
	case "GitRepository", "OCIRepository", "HelmRepository":
		url, _, _ := unstructured.NestedString(src.Object, "spec", "url")
		return url
	case "Bucket":
		endpoint, _, _ := unstructured.NestedString(src.Object, "spec", "endpoint")
		bucket, _, _ := unstructured.NestedString(src.Object, "spec", "bucketName")
		if endpoint != "" && bucket != "" {
			return strings.TrimRight(endpoint, "/") + "/" + bucket
		}
	}
	return ""
}

// safeRepo drops credentials, a query and a fragment from a repository
// address. Only the conventional git user survives (ssh://git@ and
// git@host:org/repo), since any other user name can be a token. An address
// without a scheme (an scp-style one, or a Flux Bucket's endpoint) loses its
// query, fragment and any other user the same way.
func safeRepo(repo string) string {
	if !strings.Contains(repo, "://") {
		// No scheme to parse by, so a ? or # can't be told from part of a
		// credential: such an address is left out. A git@ address keeps
		// its path whole (an @ there is part of it); any other user is
		// dropped when one @ marks it, and with several @ the address is
		// ambiguous and left out.
		if strings.ContainsAny(repo, "?#") {
			return ""
		}
		if strings.HasPrefix(repo, "git@") {
			return repo
		}
		switch strings.Count(repo, "@") {
		case 0:
			return repo
		case 1:
			_, rest, _ := strings.Cut(repo, "@")
			return rest
		default:
			return ""
		}
	}
	if u, err := url.Parse(repo); err == nil && u.Scheme == "ssh" && u.User != nil && u.User.Username() == "git" {
		if v, err := url.Parse(prom.SafeAddress(repo)); err == nil {
			v.User = url.User("git")
			return v.String()
		}
	}
	return prom.SafeAddress(repo)
}

// ownedWorkloads are the workloads resp names, once each, without
// containers, in a stable order.
func ownedWorkloads(resp *Response) []WorkloadRef {
	seen := map[WorkloadRef]bool{}
	add := func(ws ...WorkloadRef) {
		for _, w := range ws {
			w.Container = ""
			seen[w] = true
		}
	}
	addExposed := func(es []ExposedWorkload) {
		for _, e := range es {
			add(e.WorkloadRef)
		}
	}
	for _, img := range resp.Images {
		add(img.Workloads...)
	}
	for _, c := range resp.Clean {
		add(c.Workloads...)
	}
	for _, c := range resp.CVEs {
		addExposed(c.Exposed)
		for _, a := range c.Affected {
			add(a.Workloads...)
		}
	}
	for _, f := range resp.Fixes {
		addExposed(f.Exposed)
		for _, a := range f.Affected {
			add(a.Workloads...)
		}
	}
	workloadRows := func(rows []Workload) {
		for _, w := range rows {
			add(WorkloadRef{Kind: w.Kind, Group: w.Group, Namespace: w.Namespace, Name: w.Name})
		}
	}
	workloadRows(resp.Workloads)
	add(resp.CleanWorkloads...)
	for _, u := range resp.Unscanned {
		add(u.Workloads...)
	}
	if x := resp.export; x != nil {
		workloadRows(x.cleanWorkloads)
		workloadRows(x.unscannedWorkloads)
		for _, u := range x.unread {
			add(u.workloads...)
		}
	}
	out := make([]WorkloadRef, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	slices.SortFunc(out, compareWorkloadRefs)
	return out
}

func compareWorkloadRefs(a, b WorkloadRef) int {
	return strings.Compare(a.Namespace+"/"+a.Group+"/"+a.Kind+"/"+a.Name, b.Namespace+"/"+b.Group+"/"+b.Kind+"/"+b.Name)
}

// ownerIndex looks a workload's owner up, container ignored. When no owner
// could be read at all (Ownership.Unavailable), every workload's is
// unknown, not "none found".
func ownerIndex(resp *Response) func(WorkloadRef) *WorkloadOwner {
	out := make(map[WorkloadRef]*WorkloadOwner, len(resp.Owners))
	for i := range resp.Owners {
		out[resp.Owners[i].WorkloadRef] = &resp.Owners[i]
	}
	unavailable := resp.Ownership != nil && resp.Ownership.Unavailable != ""
	return func(w WorkloadRef) *WorkloadOwner {
		if o := out[w]; o != nil || !unavailable {
			return o
		}
		return &WorkloadOwner{WorkloadRef: w, ManagerUnknown: true}
	}
}

// ownerCells are a workload's owner_kind, owner, source_repo, source_path,
// chart and team cells. A workload Radar names no manager for has the
// first five empty; "unknown" in owner_kind says the evidence didn't
// settle it, never that it has none.
func ownerCells(o *WorkloadOwner, join func([]string) string) []string {
	if o == nil {
		return make([]string, 6)
	}
	cells := make([]string, 6)
	switch m := o.Manager; {
	case m != nil:
		cells[0] = ownerKindCell(m)
		cells[1] = m.Namespace + "/" + m.Name
		// Paths line up with repositories, an empty one kept in its place,
		// so a reader can tell which repository a path is in.
		var repos, paths []string
		anyPath := false
		for _, s := range m.Sources {
			repos = append(repos, s.Repo)
			paths = append(paths, s.Path)
			anyPath = anyPath || s.Path != ""
		}
		if !anyPath {
			paths = nil
		}
		cells[2], cells[3], cells[4] = join(repos), join(paths), m.Chart
	case o.ManagerUnknown:
		cells[0] = "unknown"
	}
	cells[5] = o.Team
	return cells
}

func ownerKindCell(m *OwnerManager) string {
	switch {
	case m.Tool == "helm":
		return "helm_release"
	case m.Tool == "argocd":
		return "argocd_application"
	case m.Kind == "Kustomization":
		return "flux_kustomization"
	default:
		return "flux_helmrelease"
	}
}

// PruneOwners keeps the owners of the workloads r still names, for a
// caller that trims its rows after Read.
func (r *Response) PruneOwners() {
	if len(r.Owners) == 0 {
		return
	}
	named := map[WorkloadRef]bool{}
	for _, w := range ownedWorkloads(r) {
		named[w] = true
	}
	r.Owners = slices.DeleteFunc(slices.Clone(r.Owners), func(o WorkloadOwner) bool { return !named[o.WorkloadRef] })
}
