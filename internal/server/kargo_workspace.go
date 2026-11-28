package server

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/conditions"
	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/kargo"
)

// The Kargo kinds the workspace reads, each authorized on its own. Kargo keeps
// one project per namespace, so a project is read as that namespace.
var (
	kargoStagesKind     = integration.WorkspaceKind{Key: "stages", Group: kargo.Group, Kind: kargo.KindStage, Resource: "stages"}
	kargoFreightKind    = integration.WorkspaceKind{Key: "freight", Group: kargo.Group, Kind: kargo.KindFreight, Resource: "freights"}
	kargoWarehousesKind = integration.WorkspaceKind{Key: "warehouses", Group: kargo.Group, Kind: kargo.KindWarehouse, Resource: "warehouses"}
	kargoProjectsKind   = integration.WorkspaceKind{Key: "projects", Group: kargo.Group, Kind: kargo.KindProject, Resource: "projects", ClusterScoped: true}
	kargoArgoAppsKind   = integration.WorkspaceKind{Key: "applications", Group: argoGroup, Kind: "Application", Resource: "applications"}
)

const argoGroup = "argoproj.io"

// kargoFreightPerWarehouse bounds the Freight a project answer carries for
// each Warehouse: its newest, so a Warehouse's own list is never crowded out
// by another's. Kargo's garbage collector keeps about 20 per Warehouse beyond
// those in use, so the cap is rarely reached; Freight a Stage holds is always
// included. Warehouses[].freightCount says how many each has in all.
const kargoFreightPerWarehouse = 50

// KargoWorkspaceResponse is GET /api/kargo/workspace: the projects the caller
// can read Stages in, for the project picker and the sidebar count.
type KargoWorkspaceResponse struct {
	Installed bool                  `json:"installed"`
	Projects  []KargoProjectSummary `json:"projects"`
	// Coverage has "stages": where the caller's projects were looked for.
	Coverage map[string]integration.KindCoverage `json:"coverage"`
}

// KargoProjectSummary is one project in the picker.
type KargoProjectSummary struct {
	Name   string `json:"name"`
	Stages int    `json:"stages"`
	// Problems counts the project's Kargo findings in Issues, each once.
	Problems int `json:"problems"`
	// DeployNamespaces are the namespaces its Stages' Argo CD Applications
	// deploy to in this cluster, from the Applications the caller can read.
	DeployNamespaces []string `json:"deployNamespaces,omitempty"`
}

// KargoProjectResponse is GET /api/kargo/projects/{project}.
type KargoProjectResponse struct {
	Name string `json:"name"`
	// Coverage has "stages", "freight" and "warehouses" for this project.
	Coverage map[string]integration.KindCoverage `json:"coverage"`
	// FreightRead: the caller can read this project's Freight, so aliases and
	// "since" are known. Without it, versions still come from the Stages.
	FreightRead bool             `json:"freightRead"`
	Stages      []KargoStageView `json:"stages"`
	Warehouses  []KargoWarehouse `json:"warehouses"`
	Freight     []map[string]any `json:"freight"`
	// FreightByWarehouse is how many Freight each Warehouse has in all, where
	// Freight lists only each one's newest. Set when Freight was read.
	FreightByWarehouse map[string]int `json:"freightByWarehouse,omitempty"`
	FreightTotal       int            `json:"freightTotal"`
	// VerificationRuns are each Freight's verification runs by Freight name,
	// read from the Stages' freight history. Absent when Stages are unread.
	VerificationRuns map[string][]KargoVerificationRun `json:"verificationRuns,omitempty"`
	// Issues are the project's Kargo findings from Issues.
	Issues []issues.Issue `json:"issues"`
}

// KargoFreightRefView is a Freight as a card shows it.
type KargoFreightRefView struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
	// Alias is set only when the caller can read the Freight.
	Alias    string   `json:"alias,omitempty"`
	Versions []string `json:"versions"`
	// Since is when the Stage received it (Kargo v1.12 Freight status);
	// empty when unknown.
	Since string `json:"since,omitempty"`
}

// KargoStageView is one Stage card.
type KargoStageView struct {
	Name            string                `json:"name"`
	Depth           int                   `json:"depth"`
	Upstream        []string              `json:"upstream,omitempty"`
	Warehouses      []string              `json:"warehouses,omitempty"`
	MissingUpstream []string              `json:"missingUpstream,omitempty"`
	Phase           string                `json:"phase"`
	ControlFlow     bool                  `json:"controlFlow,omitempty"`
	AutoPromotion   bool                  `json:"autoPromotion"`
	Current         []KargoFreightRefView `json:"current"`
	Health          *KargoHealthView      `json:"health,omitempty"`
	// VerificationConfigured: the Stage declares verification.
	VerificationConfigured bool                   `json:"verificationConfigured"`
	Verification           *KargoVerificationView `json:"verification,omitempty"`
	LastPromotion          *KargoPromotionView    `json:"lastPromotion,omitempty"`
	CurrentPromotion       *KargoPromotionView    `json:"currentPromotion,omitempty"`
	ArgoCDApps             []KargoArgoCDApp       `json:"argoCDApps,omitempty"`
	WhyNotHere             []KargoLagView         `json:"whyNotHere"`
}

type KargoHealthView struct {
	Status string   `json:"status"`
	Issues []string `json:"issues,omitempty"`
}

type KargoVerificationView struct {
	Phase       string           `json:"phase"`
	Message     string           `json:"message,omitempty"`
	AnalysisRun *kargoObjectView `json:"analysisRun,omitempty"`
	FinishedAt  string           `json:"finishedAt,omitempty"`
}

type kargoObjectView struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type KargoPromotionView struct {
	Name       string `json:"name"`
	Phase      string `json:"phase,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Freight    string `json:"freight,omitempty"`
	// FailedStep and Failure explain a Failed or Errored promotion; Failure is
	// empty when Kargo recorded no reason. FailureCause is Failure without
	// Kargo's own wrappers around the step's error, for a short line.
	FailedStep   string `json:"failedStep,omitempty"`
	Failure      string `json:"failure,omitempty"`
	FailureCause string `json:"failureCause,omitempty"`
}

// KargoArgoCDApp is an Argo CD Application the Stage's last promotion
// updated, with its health as the Stage's health check last read it.
type KargoArgoCDApp struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Health    string `json:"health,omitempty"`
	Sync      string `json:"sync,omitempty"`
}

// KargoLagView answers, for one origin, why the newest Freight its sources
// offer is not in the Stage.
type KargoLagView struct {
	Origin    string               `json:"origin"`
	State     string               `json:"state"`
	Reason    string               `json:"reason"`
	Cause     string               `json:"cause,omitempty"`
	Message   string               `json:"message"`
	Newest    *KargoFreightRefView `json:"newest,omitempty"`
	Stages    []string             `json:"stages,omitempty"`
	Promotion string               `json:"promotion,omitempty"`
	// AnalysisRuns are the failed upstream verifications' AnalysisRuns.
	AnalysisRuns []kargoObjectView `json:"analysisRuns,omitempty"`
}

// KargoWarehouse is one Warehouse with what the workspace shows of it.
type KargoWarehouse struct {
	Name string `json:"name"`
	// Conditions are the Warehouse's Ready and Healthy conditions.
	Conditions []map[string]any `json:"conditions,omitempty"`
	// Latest is the newest Freight it produced; FreightCount how many of its
	// Freight the project holds. Both need Freight access.
	Latest       *KargoFreightRefView `json:"latest,omitempty"`
	FreightCount int                  `json:"freightCount"`
}

// KargoVerificationRun is one verification of a Freight in a Stage.
type KargoVerificationRun struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Stage      string `json:"stage"`
	Phase      string `json:"phase,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// KargoFreightAliasesResponse is GET /api/kargo/freight-aliases.
type KargoFreightAliasesResponse struct {
	// Aliases maps "namespace/freightName" to the Freight's alias.
	Aliases  map[string]string        `json:"aliases"`
	Coverage integration.KindCoverage `json:"coverage"`
}

// kargoScope is where a Kargo read may look: nil means every namespace the
// caller may read; a forced --namespace-scope narrows it to its target.
func kargoScope() []string {
	if !k8s.ForceNamespaceScope {
		return nil
	}
	if target := k8s.GetNamespaceScopeTarget(); target != "" {
		return []string{target}
	}
	return []string{}
}

func kargoInstalled() bool {
	d := k8s.GetResourceDiscovery()
	if d == nil {
		return false
	}
	_, ok := d.GetGVRWithGroup(kargo.KindStage, kargo.Group)
	return ok
}

// handleKargoWorkspace serves GET /api/kargo/workspace: the projects the
// caller can read Stages in, with each project's Kargo findings counted once.
//
// The namespace picker is not applied: the workspace picks a project itself.
// Each kind is still authorized per namespace on the caller's own access, and
// the --namespace-scope pin bounds the read.
func (s *Server) handleKargoWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) || s.writeIfDiscoveryPending(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	resp := KargoWorkspaceResponse{Projects: []KargoProjectSummary{}, Coverage: map[string]integration.KindCoverage{}}
	if resp.Installed = kargoInstalled(); !resp.Installed {
		s.writeJSON(w, resp)
		return
	}
	budget := newSyncBudget(r.Context())
	budget.readOnly = true
	scope := kargoScope()

	// A caller without cluster-wide list is checked only in Kargo's projects
	// as Radar holds them: the namespaces of its Stages and its Project
	// objects. That costs a few access checks and needs no Namespace list.
	projectNames := kargoProjectsHeld(r.Context(), cache, budget)
	radarDenied := false
	held := func() []string {
		namespaces, denied := kargoNamespacesHeld(r.Context(), cache, budget)
		radarDenied = denied
		return mergeSorted(namespaces, projectNames)
	}
	stageAccess, stages := s.readWorkspaceKindAmong(r, cache, kargoStagesKind, scope, []string{kargo.Group}, budget, held, true)
	// With nothing held because Radar's own identity can't list Stages, a
	// caller checked among the held projects reads denied; the missing access
	// is Radar's.
	if stageAccess.State == integration.KindCoverageDenied && radarDenied && len(projectNames) == 0 {
		stageAccess = integration.KindAccess{State: integration.KindCoverageUncached}
	}
	resp.Coverage[kargoStagesKind.Key] = stageAccess.Coverage()
	byProject := map[string]*KargoProjectSummary{}
	project := func(name string) *KargoProjectSummary {
		if p, ok := byProject[name]; ok {
			return p
		}
		p := &KargoProjectSummary{Name: name}
		byProject[name] = p
		return p
	}
	apps := map[string][]kargo.ObjectRef{}
	for _, u := range stages {
		if kargo.Is(u, kargo.KindStage) {
			project(u.GetNamespace()).Stages++
			refs, _ := kargo.ArgoCDApps(u)
			apps[u.GetNamespace()] = append(apps[u.GetNamespace()], refs...)
		}
	}
	for name, namespaces := range s.kargoDeployNamespaces(r, cache, budget, apps) {
		project(name).DeployNamespaces = namespaces
	}
	// A Project without Stages yet is still a project. It is listed to a
	// caller who may list Stages in its namespace, which is where its
	// pipeline would be; its name is that namespace's, so the Project object
	// itself, read with Radar's identity, adds nothing else.
	for _, name := range projectNames {
		if (scope != nil && !slices.Contains(scope, name)) || !stageAccess.Covers(name) {
			continue
		}
		project(name)
	}

	names := make([]string, 0, len(byProject))
	for name := range byProject {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, iss := range s.kargoIssues(r, names) {
		if p, ok := byProject[iss.Namespace]; ok {
			p.Problems++
		}
	}
	for _, name := range names {
		resp.Projects = append(resp.Projects, *byProject[name])
	}
	s.writeJSON(w, resp)
}

// kargoDeployNamespaces are, per project, the in-cluster namespaces the
// Argo CD Applications its Stages name (argocd-context) deploy to, read from
// the Applications the caller may list.
func (s *Server) kargoDeployNamespaces(r *http.Request, cache *k8s.ResourceCache, budget *syncBudget, apps map[string][]kargo.ObjectRef) map[string][]string {
	var appNamespaces []string
	for _, refs := range apps {
		for _, a := range refs {
			if a.Namespace != "" && !slices.Contains(appNamespaces, a.Namespace) {
				appNamespaces = append(appNamespaces, a.Namespace)
			}
		}
	}
	if len(appNamespaces) == 0 {
		return nil
	}
	sort.Strings(appNamespaces)
	_, items := s.readWorkspaceKind(r, cache, kargoArgoAppsKind, appNamespaces, []string{argoGroup}, budget)
	dest := map[kargo.ObjectRef]string{}
	for _, u := range items {
		if u.GetKind() != kargoArgoAppsKind.Kind || !gitops.IsInClusterDestination(u) {
			continue
		}
		if ns, _, _ := unstructured.NestedString(u.Object, "spec", "destination", "namespace"); ns != "" {
			dest[kargo.ObjectRef{Namespace: u.GetNamespace(), Name: u.GetName()}] = ns
		}
	}
	out := map[string][]string{}
	for project, refs := range apps {
		for _, a := range refs {
			if ns, ok := dest[a]; ok && !slices.Contains(out[project], ns) {
				out[project] = append(out[project], ns)
			}
		}
		sort.Strings(out[project])
	}
	return out
}

// kargoNamespacesHeld are the namespaces Radar's own identity holds Stages
// in: the projects a caller without cluster-wide list is checked in.
// radarDenied: Radar's identity may not list Stages anywhere.
func kargoNamespacesHeld(ctx context.Context, cache *k8s.ResourceCache, budget *syncBudget) (namespaces []string, radarDenied bool) {
	list, err := listKindInGroups(ctx, cache, kargoStagesKind, nil, []string{kargo.Group}, budget)
	if err != nil {
		if _, _, watched, ok := dynamicNamespaceWatches(kargoStagesKind, budget); ok {
			return watched, false
		}
		return nil, apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
	}
	seen := map[string]bool{}
	var out []string
	for _, u := range list {
		if ns := u.GetNamespace(); ns != "" && !seen[ns] {
			seen[ns] = true
			out = append(out, ns)
		}
	}
	sort.Strings(out)
	return out, false
}

// kargoProjectsHeld are the Kargo Project names Radar's own identity holds;
// none when it can't list them.
func kargoProjectsHeld(ctx context.Context, cache *k8s.ResourceCache, budget *syncBudget) []string {
	list, err := listKindInGroups(ctx, cache, kargoProjectsKind, nil, []string{kargo.Group}, budget)
	if err != nil {
		return nil
	}
	var out []string
	for _, u := range list {
		if kargo.Is(u, kargo.KindProject) {
			out = append(out, u.GetName())
		}
	}
	sort.Strings(out)
	return out
}

func mergeSorted(a, b []string) []string {
	out := slices.Concat(a, b)
	sort.Strings(out)
	return slices.Compact(out)
}

// kargoIssues are the Kargo findings Issues holds in namespaces, authorized
// as the Issues page authorizes them.
func (s *Server) kargoIssues(r *http.Request, namespaces []string) []issues.Issue {
	if len(namespaces) == 0 || readscope.NoNamespaceAccess(namespaces) {
		return nil
	}
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	rows, _ := issues.ComposeWithStats(provider, issues.Filters{
		Namespaces:             namespaces,
		Limit:                  issues.NoLimit,
		SkipPodTemplateContext: true,
		CanReadClusterScoped:   s.issueClusterScopedAccess(r),
		CanReadRelated:         s.issueRelatedResourceAccess(r),
		CanReadSubject:         s.issueSubjectAccess(r),
		CanReadEvidence:        s.issueEvidenceAccess(r),
	})
	out := []issues.Issue{}
	for _, iss := range rows {
		if iss.Group == kargo.Group {
			out = append(out, iss)
		}
	}
	return out
}

// handleKargoProject serves GET /api/kargo/projects/{project}: the project's
// Stages in pipeline order, each with the Freight it holds per origin, health,
// verification, last promotion and why the newest Freight isn't there yet,
// plus its Warehouses, Freight and Kargo findings.
//
// Stages, Freight and Warehouses are authorized on their own. With Stages
// readable and Freight denied, versions still come from the Stages and the
// aliases are unavailable; a denied kind reads "denied", never "none".
func (s *Server) handleKargoProject(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	project := chi.URLParam(r, "project")
	if len(validation.IsDNS1123Label(project)) > 0 {
		s.writeError(w, http.StatusBadRequest, "invalid project name")
		return
	}
	if outsideNamespacePin(project) != nil {
		s.writeError(w, http.StatusForbidden, "project is outside Radar's namespace scope")
		return
	}
	if s.writeIfDiscoveryPending(w) {
		return
	}
	if !kargoInstalled() {
		s.writeError(w, http.StatusNotFound, "Kargo is not installed")
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	budget := newSyncBudget(r.Context())
	budget.readOnly = true
	scope := []string{project}
	read := func(k integration.WorkspaceKind) (integration.KindAccess, []*unstructured.Unstructured) {
		acc, items := s.readWorkspaceKind(r, cache, k, scope, []string{kargo.Group}, budget)
		kept := items[:0:0]
		for _, u := range items {
			if kargo.Is(u, k.Kind) && u.GetNamespace() == project {
				kept = append(kept, u)
			}
		}
		return acc, kept
	}
	stageAccess, stageObjs := read(kargoStagesKind)
	freightAccess, freightObjs := read(kargoFreightKind)
	warehouseAccess, warehouseObjs := read(kargoWarehousesKind)

	resp := buildKargoProject(project, kargoProjectInputs{
		stagesRead:     stageAccess.Covers(project),
		stages:         stageObjs,
		freightRead:    freightAccess.Covers(project),
		freight:        freightObjs,
		warehousesRead: warehouseAccess.Covers(project),
		warehouses:     warehouseObjs,
	}, time.Now())
	resp.Coverage = map[string]integration.KindCoverage{
		kargoStagesKind.Key:     stageAccess.Coverage(),
		kargoFreightKind.Key:    freightAccess.Coverage(),
		kargoWarehousesKind.Key: warehouseAccess.Coverage(),
	}
	if resp.Issues = []issues.Issue{}; stageAccess.Covers(project) {
		resp.Issues = s.kargoIssues(r, scope)
	}
	s.writeJSON(w, resp)
}

type kargoProjectInputs struct {
	stagesRead, freightRead, warehousesRead bool
	stages, freight, warehouses             []*unstructured.Unstructured
}

// buildKargoProject composes the project answer from what the caller could
// read. It reads only objects, so it is tested without a server.
func buildKargoProject(project string, in kargoProjectInputs, now time.Time) KargoProjectResponse {
	resp := KargoProjectResponse{Name: project, FreightRead: in.freightRead, Stages: []KargoStageView{}, Warehouses: []KargoWarehouse{}, Freight: []map[string]any{}}
	index := kargo.IndexFreight(in.freight)

	parsed := make([]kargo.Stage, 0, len(in.stages))
	objects := map[string]*unstructured.Unstructured{}
	for _, u := range in.stages {
		parsed = append(parsed, kargo.ParseStage(u))
		objects[u.GetName()] = u
	}
	proj := kargo.NewProject(parsed, in.freight, in.freightRead)
	byName := map[string]kargo.Stage{}
	for _, st := range parsed {
		byName[st.Name] = st
	}
	refView := func(f kargo.FreightRef, stage string) KargoFreightRefView {
		v := KargoFreightRefView{Name: f.Name, Origin: f.Origin.Name, Versions: f.Labels()}
		if v.Versions == nil {
			v.Versions = []string{}
		}
		if in.freightRead {
			if obj := index.Object(f.Name); obj != nil {
				v.Alias = kargo.Alias(obj)
				if stage != "" {
					if since, ok := kargo.Since(obj, stage); ok {
						v.Since = since.UTC().Format(time.RFC3339)
					}
				}
			}
		}
		return v
	}

	held := map[string]bool{}
	for _, p := range kargo.Pipeline(parsed) {
		st := byName[p.Name]
		obj := objects[p.Name]
		view := KargoStageView{
			Name:                   st.Name,
			Depth:                  p.Depth,
			Upstream:               p.Upstream,
			Warehouses:             p.Warehouses,
			MissingUpstream:        p.MissingUpstream,
			Phase:                  kargo.Phase(obj),
			ControlFlow:            st.ControlFlow,
			AutoPromotion:          st.AutoPromotion,
			Current:                []KargoFreightRefView{},
			VerificationConfigured: kargoVerificationConfigured(obj),
			WhyNotHere:             []KargoLagView{},
		}
		for _, f := range st.Current {
			held[f.Name] = true
			view.Current = append(view.Current, refView(f, st.Name))
		}
		if h := st.Health; h != nil {
			view.Health = &KargoHealthView{Status: h.Status, Issues: h.Issues}
		}
		if v := st.Verification; v != nil {
			view.Verification = &KargoVerificationView{Phase: v.Phase, Message: v.Message, FinishedAt: rfc3339(v.FinishTime)}
			if v.AnalysisRun != nil {
				view.Verification.AnalysisRun = &kargoObjectView{Namespace: cmp.Or(v.AnalysisRun.Namespace, st.Namespace), Name: v.AnalysisRun.Name}
			}
		}
		view.LastPromotion = kargoPromotionView(st.LastPromotion)
		view.CurrentPromotion = kargoPromotionView(st.CurrentPromotion)
		for _, app := range st.ArgoCDApps {
			a := KargoArgoCDApp{Namespace: app.Namespace, Name: app.Name}
			if st.Health != nil {
				for _, status := range st.Health.Apps {
					if status.Name == app.Name && (status.Namespace == "" || status.Namespace == app.Namespace) {
						a.Health, a.Sync = status.Health, status.Sync
					}
				}
			}
			view.ArgoCDApps = append(view.ArgoCDApps, a)
		}
		for _, lag := range kargo.WhyNotHere(st, proj, now) {
			lv := KargoLagView{Origin: lag.Origin.Name, State: string(lag.State), Reason: string(lag.Reason), Cause: string(lag.Cause), Message: lag.Message, Stages: lag.Stages, Promotion: lag.Promotion}
			for _, run := range lag.AnalysisRuns {
				lv.AnalysisRuns = append(lv.AnalysisRuns, kargoObjectView{Namespace: cmp.Or(run.Namespace, st.Namespace), Name: run.Name})
			}
			if lag.Newest != nil {
				newest := refView(*lag.Newest, "")
				lv.Newest = &newest
			}
			view.WhyNotHere = append(view.WhyNotHere, lv)
		}
		resp.Stages = append(resp.Stages, view)
	}
	if in.stagesRead {
		resp.VerificationRuns = kargoVerificationRuns(in.stages)
	}

	if in.freightRead {
		all := make([]kargo.Freight, 0, len(in.freight))
		for _, u := range in.freight {
			all = append(all, kargo.ParseFreight(u))
		}
		slices.SortFunc(all, func(a, b kargo.Freight) int {
			if c := b.DiscoveredAt.Compare(a.DiscoveredAt); c != 0 {
				return c
			}
			return cmp.Compare(a.Name, b.Name)
		})
		resp.FreightTotal = len(all)
		perOrigin := map[kargo.Origin]int{}
		resp.FreightByWarehouse = map[string]int{}
		for _, f := range all {
			perOrigin[f.Origin]++
			if f.Origin.Kind == kargo.KindWarehouse {
				resp.FreightByWarehouse[f.Origin.Name]++
			}
			if perOrigin[f.Origin] <= kargoFreightPerWarehouse || held[f.Name] {
				resp.Freight = append(resp.Freight, kargoFreightObject(index.Object(f.Name)))
			}
		}
	}

	for _, u := range in.warehouses {
		wh := KargoWarehouse{Name: u.GetName()}
		for _, t := range []string{"Ready", "Healthy"} {
			if c, ok := conditions.Find(u, t); ok {
				wh.Conditions = append(wh.Conditions, map[string]any{"type": t, "status": c.Status, "reason": c.Reason, "message": c.Message})
			}
		}
		if in.freightRead {
			newest := index.Newest(kargo.Origin{Kind: kargo.KindWarehouse, Name: u.GetName()})
			wh.FreightCount = len(newest)
			if len(newest) > 0 {
				f := newest[0]
				v := refView(kargo.FreightRef{Name: f.Name, Origin: f.Origin, Versions: f.Versions}, "")
				wh.Latest = &v
			}
		}
		resp.Warehouses = append(resp.Warehouses, wh)
	}
	slices.SortFunc(resp.Warehouses, func(a, b KargoWarehouse) int { return cmp.Compare(a.Name, b.Name) })
	return resp
}

func kargoPromotionView(r *kargo.PromotionRecord) *KargoPromotionView {
	if r == nil {
		return nil
	}
	v := &KargoPromotionView{Name: r.Name, Phase: r.Phase, FinishedAt: rfc3339(r.FinishedAt)}
	if r.Freight != nil {
		v.Freight = r.Freight.Name
	}
	if f := r.Failure(); f.Failed {
		v.FailedStep, v.Failure, v.FailureCause = f.StepAlias, strings.TrimSpace(f.Message), kargo.Cause(f.Message)
	}
	return v
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// kargoVerificationConfigured: the Stage declares verification
// (spec.verification.analysisTemplates). v1alpha1 field read here rather
// than in pkg/kargo, which doesn't expose it.
func kargoVerificationConfigured(u *unstructured.Unstructured) bool {
	if u == nil {
		return false
	}
	templates, _, _ := unstructured.NestedSlice(u.Object, "spec", "verification", "analysisTemplates")
	return len(templates) > 0
}

// kargoVerificationRuns lists, per Freight, the verification runs the Stages'
// freight history records: every collection holding the Freight, every
// verification of that collection that ran as an AnalysisRun. v1alpha1 field
// paths (status.freightHistory[].items, .verificationHistory[]).
func kargoVerificationRuns(stages []*unstructured.Unstructured) map[string][]KargoVerificationRun {
	out := map[string][]KargoVerificationRun{}
	seen := map[string]bool{}
	for _, u := range stages {
		history, _, _ := unstructured.NestedSlice(u.Object, "status", "freightHistory")
		for _, raw := range history {
			collection, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			items, _, _ := unstructured.NestedMap(collection, "items")
			verifications, _, _ := unstructured.NestedSlice(collection, "verificationHistory")
			for _, rawItem := range items {
				item, ok := rawItem.(map[string]any)
				if !ok {
					continue
				}
				freight, _, _ := unstructured.NestedString(item, "name")
				if freight == "" {
					continue
				}
				for _, rawV := range verifications {
					v, ok := rawV.(map[string]any)
					if !ok {
						continue
					}
					name, _, _ := unstructured.NestedString(v, "analysisRun", "name")
					if name == "" {
						continue
					}
					ns, _, _ := unstructured.NestedString(v, "analysisRun", "namespace")
					ns = cmp.Or(ns, u.GetNamespace())
					key := freight + "\x00" + ns + "/" + name
					if seen[key] {
						continue
					}
					seen[key] = true
					phase, _, _ := unstructured.NestedString(v, "phase")
					finished, _, _ := unstructured.NestedString(v, "finishTime")
					out[freight] = append(out[freight], KargoVerificationRun{Name: name, Namespace: ns, Stage: u.GetName(), Phase: phase, FinishedAt: finished})
				}
			}
		}
	}
	for name := range out {
		slices.SortFunc(out[name], func(a, b KargoVerificationRun) int { return cmp.Compare(b.FinishedAt, a.FinishedAt) })
	}
	return out
}

// kargoFreightObject is the Freight as the Freight renderer reads it: its
// identity, alias, origin, versions and status. Annotations, managed fields
// and owner references are left out.
func kargoFreightObject(u *unstructured.Unstructured) map[string]any {
	if u == nil {
		return nil
	}
	meta := map[string]any{"name": u.GetName(), "namespace": u.GetNamespace()}
	if ts := u.GetCreationTimestamp(); !ts.IsZero() {
		meta["creationTimestamp"] = ts.UTC().Format(time.RFC3339)
	}
	if alias := u.GetLabels()[kargo.AliasLabel]; alias != "" {
		meta["labels"] = map[string]any{kargo.AliasLabel: alias}
	}
	out := map[string]any{"apiVersion": u.GetAPIVersion(), "kind": u.GetKind(), "metadata": meta}
	for _, field := range []string{"alias", "origin", "discoveredAt", "images", "commits", "charts", "artifacts", "status"} {
		if v, ok := u.Object[field]; ok {
			out[field] = v
		}
	}
	return out
}

// handleKargoFreightAliases serves GET /api/kargo/freight-aliases: the alias
// of each Freight the caller can read in the view's namespaces, for the Stage
// and Promotion lists. Denied namespaces carry no aliases; the lists then show
// versions only.
func (s *Server) handleKargoFreightAliases(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) || s.writeIfDiscoveryPending(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	resp := KargoFreightAliasesResponse{Aliases: map[string]string{}}
	if !kargoInstalled() {
		resp.Coverage = integration.KindCoverage{State: integration.KindCoverageNotInstalled}
		s.writeJSON(w, resp)
		return
	}
	budget := newSyncBudget(r.Context())
	budget.readOnly = true
	access, items := s.readWorkspaceKind(r, cache, kargoFreightKind, s.parseNamespacesForUser(r), []string{kargo.Group}, budget)
	resp.Coverage = access.Coverage()
	for _, u := range items {
		if !kargo.Is(u, kargo.KindFreight) {
			continue
		}
		if alias := kargo.Alias(u); alias != "" {
			resp.Aliases[u.GetNamespace()+"/"+u.GetName()] = alias
		}
	}
	s.writeJSON(w, resp)
}
