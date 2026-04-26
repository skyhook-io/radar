package server

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/packages"
)

// packagesCacheTTL bounds how often we recompute the merged package
// list. Aggregate is cheap; the inputs (Helm secret reads, dynamic-cache
// walks) are not.
const packagesCacheTTL = 60 * time.Second

var (
	packagesCacheMu sync.Mutex
	packagesCache   = map[string]packagesCacheEntry{}
)

type packagesCacheEntry struct {
	at     time.Time
	rows   []packages.PackageRow
	errors []SourceError
}

// PackagesResponse is the on-wire shape returned by /api/packages.
type PackagesResponse struct {
	Packages       []packages.PackageRow `json:"packages"`
	GeneratedAt    time.Time             `json:"generatedAt"`
	SourcesUsed    []string              `json:"sourcesUsed"`
	SourcesErrored []SourceError         `json:"sourcesErrored,omitempty"`
}

// SourceError carries a per-source failure. Source is one of H/L/C/A/F.
type SourceError struct {
	Source     string `json:"source"`
	StatusCode int    `json:"statusCode,omitempty"`
	Error      string `json:"error"`
}

// ListPackagesParams carries the filters the REST + MCP handlers both
// support.
type ListPackagesParams struct {
	// Namespaces filters returned rows by release-namespace.
	// nil = all namespaces. Empty (non-nil) slice = "no access" → returns
	// an empty response without consulting the cache.
	Namespaces []string
	Source     string // H/L/C/A/F or empty
	Chart      string // case-insensitive substring or empty
	// User identity for Helm release secret reads. Empty username means
	// "use the SA identity" (helm.ListReleasesAsUser convention).
	User   string
	Groups []string
}

// ListPackages is the public entry point shared by the REST handler
// and the MCP tool.
func ListPackages(ctx context.Context, p ListPackagesParams) (PackagesResponse, error) {
	// Auth-restricted to no namespaces → empty response, skip cache.
	if p.Namespaces != nil && len(p.Namespaces) == 0 {
		return PackagesResponse{
			Packages:       []packages.PackageRow{},
			GeneratedAt:    time.Now(),
			SourcesUsed:    []string{},
			SourcesErrored: nil,
		}, nil
	}

	cacheKey := packagesCacheKeyFor(p.User, p.Namespaces)
	packagesCacheMu.Lock()
	entry, hit := packagesCache[cacheKey]
	packagesCacheMu.Unlock()

	var rows []packages.PackageRow
	var sourceErrs []SourceError
	if hit && time.Since(entry.at) < packagesCacheTTL {
		rows = entry.rows
		sourceErrs = entry.errors
	} else {
		var err error
		rows, sourceErrs, err = computePackagesInternal(ctx, p.Namespaces, p.User, p.Groups)
		if err != nil {
			return PackagesResponse{}, err
		}
		packagesCacheMu.Lock()
		packagesCache[cacheKey] = packagesCacheEntry{at: time.Now(), rows: rows, errors: sourceErrs}
		packagesCacheMu.Unlock()
	}

	if p.Source != "" {
		rows = filterBySource(rows, strings.ToUpper(p.Source))
	}
	if p.Chart != "" {
		rows = filterByChartSubstring(rows, strings.ToLower(p.Chart))
	}

	if rows == nil {
		rows = []packages.PackageRow{}
	}
	used := sourcesUsed(rows)
	if used == nil {
		used = []string{}
	}
	return PackagesResponse{
		Packages:       rows,
		GeneratedAt:    time.Now(),
		SourcesUsed:    used,
		SourcesErrored: sourceErrs,
	}, nil
}

// packagesCacheKeyFor produces a stable cache key. Both the user
// identity and the requested namespace set must be part of the key:
// Helm reads are user-scoped (RBAC-impersonated), so two users hitting
// the same namespace must not share an entry.
func packagesCacheKeyFor(user string, namespaces []string) string {
	var b strings.Builder
	b.WriteString(user)
	b.WriteByte('|')
	if namespaces == nil {
		b.WriteByte('*')
	} else {
		// Sort defensively; the handler already sorts, but MCP / direct
		// callers might not.
		ns := append([]string(nil), namespaces...)
		sort.Strings(ns)
		b.WriteString(strings.Join(ns, ","))
	}
	return b.String()
}

// handleListPackages serves GET /api/packages.
//
// Query params:
//
//	?namespaces=a,b,c | ?namespace=a — limit to release-namespace ∈ set.
//	?source=H|L|C|A|F                — limit to rows where this source contributed.
//	?chart=<substr>                  — case-insensitive substring on chart name.
//
// Returns 200 even when some sources failed (per-source failures are
// attributed in `sourcesErrored`).
func (s *Server) handleListPackages(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespaces := s.parseNamespacesForUser(r)
	user, groups := userCredsForPackages(r)
	resp, err := ListPackages(r.Context(), ListPackagesParams{
		Namespaces: namespaces,
		Source:     r.URL.Query().Get("source"),
		Chart:      r.URL.Query().Get("chart"),
		User:       user,
		Groups:     groups,
	})
	if err != nil {
		if err == errResourceCacheUnavailable {
			s.writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		log.Printf("[packages] ListPackages failed: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, resp)
}

// computePackagesInternal reads from all sources, merges via
// packages.Aggregate, and post-filters by the requested namespace set.
// Per-source errors are attributed but non-fatal.
func computePackagesInternal(ctx context.Context, namespaces []string, user string, groups []string) ([]packages.PackageRow, []SourceError, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, errResourceCacheUnavailable
	}

	src := packages.Sources{}
	var errs []SourceError

	// Helm releases (source H). For multi-namespace queries we list
	// cluster-wide and rely on the post-aggregate filter — Helm's RBAC
	// impersonation already scopes results to what the user can see.
	helmNamespace := ""
	if len(namespaces) == 1 {
		helmNamespace = namespaces[0]
	}
	if hClient := helm.GetClient(); hClient != nil {
		releases, err := hClient.ListReleasesAsUser(helmNamespace, user, groups)
		switch {
		case err == nil:
			src.Helm = make([]packages.HelmRelease, 0, len(releases))
			for _, h := range releases {
				src.Helm = append(src.Helm, packages.HelmRelease{
					Name:           h.Name,
					Namespace:      h.Namespace,
					Chart:          h.Chart,
					ChartVersion:   h.ChartVersion,
					AppVersion:     h.AppVersion,
					Status:         h.Status,
					ResourceHealth: h.ResourceHealth,
				})
			}
		case helm.IsForbiddenError(err):
			errs = append(errs, SourceError{
				Source:     packages.SourceHelm,
				StatusCode: http.StatusForbidden,
				Error:      "RBAC denied (helm release secrets): " + err.Error(),
			})
		default:
			errs = append(errs, SourceError{Source: packages.SourceHelm, Error: err.Error()})
		}
	}

	// Workloads (source L) — Deployments + DaemonSets + StatefulSets.
	workloads, listerErr := collectWorkloadInputs(cache, namespaces)
	src.Workloads = workloads
	if listerErr != nil {
		errs = append(errs, SourceError{Source: packages.SourceLabels, Error: listerErr.Error()})
	}

	// CRDs (source C). Always cluster-scoped.
	if crds, err := cache.ListDynamicWithGroup(ctx, "CustomResourceDefinition", "", "apiextensions.k8s.io"); err == nil {
		src.CRDs = make([]packages.CRD, 0, len(crds))
		for _, c := range crds {
			obj := c.Object
			specMap, _ := obj["spec"].(map[string]any)
			group, _ := specMap["group"].(string)
			names, _ := specMap["names"].(map[string]any)
			kind, _ := names["kind"].(string)
			plural, _ := names["plural"].(string)
			versions, _ := specMap["versions"].([]any)
			var versionNames []string
			for _, v := range versions {
				if vm, ok := v.(map[string]any); ok {
					if name, ok := vm["name"].(string); ok {
						versionNames = append(versionNames, name)
					}
				}
			}
			src.CRDs = append(src.CRDs, packages.CRD{
				Name:     c.GetName(),
				Group:    group,
				Kind:     kind,
				Plural:   plural,
				Versions: versionNames,
			})
		}
	} else {
		errs = append(errs, SourceError{Source: packages.SourceCRDs, Error: err.Error()})
	}

	// GitOps declarations (sources A + F). Listed cluster-wide regardless
	// of the requested namespaces: Argo Apps live in `argocd` but target
	// other namespaces (and Flux HRs use spec.targetNamespace), so the
	// declaration's own namespace is the wrong filter — the post-aggregate
	// step below scopes by target namespace via row.Namespace.
	src.GitOpsDeclarations = collectGitOpsDeclarations(ctx, cache, &errs)

	rows := packages.Aggregate(src)

	// Post-aggregate namespace filter. CRD-only rows (Namespace == "")
	// are dropped from namespaced queries.
	if namespaces != nil {
		allowed := map[string]bool{}
		for _, ns := range namespaces {
			allowed[ns] = true
		}
		filtered := make([]packages.PackageRow, 0, len(rows))
		for _, r := range rows {
			if allowed[r.Namespace] {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

	return rows, errs, nil
}

// collectWorkloadInputs reads Deployments + DaemonSets + StatefulSets
// from the cache and converts them to the packages.Workload shape used
// by the merger. Only workloads with helm.sh/chart label OR
// meta.helm.sh/release-name annotation contribute. Returns the first
// lister error encountered (so the caller can attribute it to source L)
// — informer listers fail rarely (indexer issues), but per the
// CLAUDE.md backend convention we don't drop errors silently.
func collectWorkloadInputs(cache *k8s.ResourceCache, namespaces []string) ([]packages.Workload, error) {
	var out []packages.Workload
	var firstErr error
	noteErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	add := func(kind, ns, name string, lbls, anns map[string]string, health string) {
		if lbls["helm.sh/chart"] == "" && anns["meta.helm.sh/release-name"] == "" {
			return
		}
		out = append(out, packages.Workload{
			Kind:        kind,
			Namespace:   ns,
			Name:        name,
			Labels:      lbls,
			Annotations: anns,
			Health:      health,
		})
	}

	// listFor expands the namespace set into per-namespace calls (or one
	// cluster-wide call when namespaces is nil).
	forEachNamespace := func(fn func(ns string)) {
		if namespaces == nil {
			fn("")
			return
		}
		for _, ns := range namespaces {
			fn(ns)
		}
	}

	if depLister := cache.Deployments(); depLister != nil {
		forEachNamespace(func(ns string) {
			if ns == "" {
				items, err := depLister.List(labels.Everything())
				noteErr(err)
				for _, d := range items {
					add("Deployment", d.Namespace, d.Name, d.Labels, d.Annotations,
						deploymentHealth(int(d.Status.Replicas), int(d.Status.AvailableReplicas)))
				}
				return
			}
			items, err := depLister.Deployments(ns).List(labels.Everything())
			noteErr(err)
			for _, d := range items {
				add("Deployment", d.Namespace, d.Name, d.Labels, d.Annotations,
					deploymentHealth(int(d.Status.Replicas), int(d.Status.AvailableReplicas)))
			}
		})
	}
	if dsLister := cache.DaemonSets(); dsLister != nil {
		forEachNamespace(func(ns string) {
			if ns == "" {
				items, err := dsLister.List(labels.Everything())
				noteErr(err)
				for _, d := range items {
					add("DaemonSet", d.Namespace, d.Name, d.Labels, d.Annotations,
						daemonsetHealth(int(d.Status.DesiredNumberScheduled), int(d.Status.NumberReady)))
				}
				return
			}
			items, err := dsLister.DaemonSets(ns).List(labels.Everything())
			noteErr(err)
			for _, d := range items {
				add("DaemonSet", d.Namespace, d.Name, d.Labels, d.Annotations,
					daemonsetHealth(int(d.Status.DesiredNumberScheduled), int(d.Status.NumberReady)))
			}
		})
	}
	if ssLister := cache.StatefulSets(); ssLister != nil {
		forEachNamespace(func(ns string) {
			if ns == "" {
				items, err := ssLister.List(labels.Everything())
				noteErr(err)
				for _, ss := range items {
					add("StatefulSet", ss.Namespace, ss.Name, ss.Labels, ss.Annotations,
						statefulsetHealth(int(ss.Status.Replicas), int(ss.Status.ReadyReplicas)))
				}
				return
			}
			items, err := ssLister.StatefulSets(ns).List(labels.Everything())
			noteErr(err)
			for _, ss := range items {
				add("StatefulSet", ss.Namespace, ss.Name, ss.Labels, ss.Annotations,
					statefulsetHealth(int(ss.Status.Replicas), int(ss.Status.ReadyReplicas)))
			}
		})
	}
	return out, firstErr
}

func deploymentHealth(desired, available int) string {
	if desired == 0 {
		return "unknown"
	}
	if available >= desired {
		return "healthy"
	}
	if available == 0 {
		return "unhealthy"
	}
	return "degraded"
}
func daemonsetHealth(desired, ready int) string   { return deploymentHealth(desired, ready) }
func statefulsetHealth(desired, ready int) string { return deploymentHealth(desired, ready) }

// collectGitOpsDeclarations reads Argo Applications + Flux HelmReleases
// + Flux Kustomizations cluster-wide. Missing CRDs (controller not
// installed) are silently absent; real informer errors surface as
// per-source errors with controller-distinguishing messages.
func collectGitOpsDeclarations(ctx context.Context, cache *k8s.ResourceCache, errs *[]SourceError) []packages.Declaration {
	var out []packages.Declaration

	if items, err := cache.ListDynamicWithGroup(ctx, "Application", "", "argoproj.io"); err == nil {
		for _, item := range items {
			if d, ok := packages.ParseArgoApplication(item.Object); ok {
				out = append(out, d)
			}
		}
	} else if !isMissingCRDErr(err) {
		*errs = append(*errs, SourceError{Source: packages.SourceArgoCD, Error: err.Error()})
	}

	if items, err := cache.ListDynamicWithGroup(ctx, "HelmRelease", "", "helm.toolkit.fluxcd.io"); err == nil {
		for _, item := range items {
			if d, ok := packages.ParseFluxHelmRelease(item.Object); ok {
				out = append(out, d)
			}
		}
	} else if !isMissingCRDErr(err) {
		*errs = append(*errs, SourceError{Source: packages.SourceFluxCD, Error: "HelmRelease: " + err.Error()})
	}

	if items, err := cache.ListDynamicWithGroup(ctx, "Kustomization", "", "kustomize.toolkit.fluxcd.io"); err == nil {
		for _, item := range items {
			if d, ok := packages.ParseFluxKustomization(item.Object); ok {
				out = append(out, d)
			}
		}
	} else if !isMissingCRDErr(err) {
		*errs = append(*errs, SourceError{Source: packages.SourceFluxCD, Error: "Kustomization: " + err.Error()})
	}

	return out
}

// isMissingCRDErr matches the "unknown resource kind" error
// k8score returns when the requested CRD isn't installed. Pinned by
// `TestIsMissingCRDErr_PinsK8scoreErrorString` — change here breaks
// graceful degradation for clusters without ArgoCD/FluxCD.
func isMissingCRDErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "unknown resource kind")
}

func userCredsForPackages(r *http.Request) (string, []string) {
	if user := auth.UserFromContext(r.Context()); user != nil {
		return user.Username, user.Groups
	}
	return "", nil
}

func filterBySource(rows []packages.PackageRow, src string) []packages.PackageRow {
	out := make([]packages.PackageRow, 0, len(rows))
	for _, r := range rows {
		for _, s := range r.Sources {
			if s == src {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

func filterByChartSubstring(rows []packages.PackageRow, sub string) []packages.PackageRow {
	out := make([]packages.PackageRow, 0, len(rows))
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.Chart), sub) {
			out = append(out, r)
		}
	}
	return out
}

func sourcesUsed(rows []packages.PackageRow) []string {
	seen := map[string]bool{}
	for _, r := range rows {
		for _, s := range r.Sources {
			seen[s] = true
		}
	}
	out := make([]string, 0, len(seen))
	for _, s := range []string{packages.SourceHelm, packages.SourceLabels, packages.SourceCRDs, packages.SourceArgoCD, packages.SourceFluxCD} {
		if seen[s] {
			out = append(out, s)
		}
	}
	return out
}

var errResourceCacheUnavailable = packagesError("resource cache unavailable")

type packagesError string

func (e packagesError) Error() string { return string(e) }
