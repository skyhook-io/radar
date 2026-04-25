package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
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
// list. Aggregate is cheap (~ms even on a 100-CRD cluster) but the
// inputs cost: Helm secret reads, dynamic-cache walks, etc. The
// dashboard endpoint uses the same 5s TTL pattern; align here.
const packagesCacheTTL = 60 * time.Second

// packagesCache holds the most recent /api/packages response, with a
// per-namespace cache key. Single-value cache (not LRU) — only one
// "all namespaces" + a few namespaced views in active use at once.
var (
	packagesCacheMu sync.Mutex
	packagesCache   = map[string]packagesCacheEntry{}
)

type packagesCacheEntry struct {
	at   time.Time
	rows []packages.PackageRow
}

// PackagesResponse is the on-wire shape returned by /api/packages.
type PackagesResponse struct {
	Packages    []packages.PackageRow `json:"packages"`
	GeneratedAt time.Time             `json:"generatedAt"`
	// SourcesUsed is the set of source codes that contributed at least
	// one row. Lets the SPA show "no Helm access — using labels + CRDs"
	// callouts when the user's RBAC limits coverage.
	SourcesUsed []string `json:"sourcesUsed"`
	// SourcesErrored carries source-level failures (e.g. Helm 403). Each
	// entry's `Source` matches the same single-character codes used in
	// PackageRow.Sources. The fleet aggregator uses this to render
	// per-cluster coverage callouts.
	SourcesErrored []SourceError `json:"sourcesErrored,omitempty"`
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
	Namespace string // empty = all namespaces
	Source    string // H/L/C/A/F or empty
	Chart     string // case-insensitive substring or empty
	// User identity for Helm release secret reads. Empty username means
	// "use the SA identity" (helm.ListReleasesAsUser convention).
	User   string
	Groups []string
}

// ListPackages is the public entry point shared by the REST handler
// and the MCP tool. Free function (not a Server method) so MCP can
// call without needing a Server reference — it relies on the same
// k8s.GetResourceCache + helm.GetClient singletons the REST handler
// reads through. Caches at the namespace level (60s TTL).
func ListPackages(ctx context.Context, p ListPackagesParams) (PackagesResponse, error) {
	cacheKey := p.Namespace
	packagesCacheMu.Lock()
	entry, hit := packagesCache[cacheKey]
	packagesCacheMu.Unlock()

	var rows []packages.PackageRow
	var sourceErrs []SourceError
	if hit && time.Since(entry.at) < packagesCacheTTL {
		rows = entry.rows
	} else {
		var err error
		rows, sourceErrs, err = computePackagesInternal(ctx, p.Namespace, p.User, p.Groups)
		if err != nil {
			return PackagesResponse{}, err
		}
		packagesCacheMu.Lock()
		packagesCache[cacheKey] = packagesCacheEntry{at: time.Now(), rows: rows}
		packagesCacheMu.Unlock()
	}

	if p.Source != "" {
		rows = filterBySource(rows, strings.ToUpper(p.Source))
	}
	if p.Chart != "" {
		rows = filterByChartSubstring(rows, strings.ToLower(p.Chart))
	}

	return PackagesResponse{
		Packages:       rows,
		GeneratedAt:    time.Now(),
		SourcesUsed:    sourcesUsed(rows),
		SourcesErrored: sourceErrs,
	}, nil
}

// handleListPackages serves GET /api/packages.
//
// Query params:
//
//	?namespace=<name> — limit to packages whose release-namespace
//	                     equals this. Default: all namespaces.
//	?source=H|L|C|A|F — limit to rows that had this source contribute.
//	?chart=<substr>   — limit to rows whose chart name contains this.
//
// Returns: PackagesResponse with the merged row list plus per-source
// error attribution. 200 even when some sources failed (we'd rather
// ship the partial view than 5xx the whole call).
func (s *Server) handleListPackages(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	user, groups := userCredsForPackages(r)
	resp, err := ListPackages(r.Context(), ListPackagesParams{
		Namespace: r.URL.Query().Get("namespace"),
		Source:    r.URL.Query().Get("source"),
		Chart:     r.URL.Query().Get("chart"),
		User:      user,
		Groups:    groups,
	})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, resp)
}

// computePackagesInternal reads from all four (potentially five) data sources,
// builds a packages.Sources, and runs Aggregate. Per-source errors are
// captured but not fatal: a 403 on Helm (secret access denied) just
// means the row set comes from L/C/A/F.
func computePackagesInternal(ctx context.Context, namespace, user string, groups []string) ([]packages.PackageRow, []SourceError, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, errResourceCacheUnavailable
	}

	src := packages.Sources{}
	var errs []SourceError

	// Helm releases (source H).
	if hClient := helm.GetClient(); hClient != nil {
		releases, err := hClient.ListReleasesAsUser(namespace, user, groups)
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
			errs = append(errs, SourceError{Source: packages.SourceHelm, StatusCode: http.StatusForbidden, Error: "RBAC denied (helm release secrets)"})
		default:
			errs = append(errs, SourceError{Source: packages.SourceHelm, Error: err.Error()})
		}
	}

	// Workloads (source L) — Deployments + DaemonSets + StatefulSets.
	src.Workloads = collectWorkloadInputs(cache, namespace)

	// CRDs (source C). Always cluster-scoped, so namespace filter
	// doesn't apply here — but we'll filter rows by namespace at the
	// end so a namespaced query doesn't surface unrelated CRD-only
	// rows.
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
				Name:    c.GetName(),
				Group:   group,
				Kind:    kind,
				Plural:  plural,
				Versions: versionNames,
			})
		}
	} else {
		errs = append(errs, SourceError{Source: packages.SourceCRDs, Error: err.Error()})
	}

	// GitOps declarations (sources A + F). All optional — controllers
	// may not be installed. Each parser tries the canonical CRD shape;
	// missing CRDs surface as ListDynamic errors, captured per-source.
	src.GitOpsDeclarations = collectGitOpsDeclarations(ctx, cache, namespace, &errs)

	rows := packages.Aggregate(src)

	// Apply namespace filter post-aggregate. CRD-only rows have empty
	// Namespace; keep them if the caller asked for "all" (namespace == "").
	if namespace != "" {
		filtered := make([]packages.PackageRow, 0, len(rows))
		for _, r := range rows {
			// Always keep rows that match the requested namespace.
			// Drop CRD-only rows in namespaced queries — the caller is
			// asking "what's in this namespace?" and CRDs are
			// cluster-scoped.
			if r.Namespace == namespace {
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
// meta.helm.sh/release-name annotation contribute; the rest aren't
// Helm-managed and the merger would skip them anyway, but filtering
// here saves a million map allocs on big clusters.
func collectWorkloadInputs(cache *k8s.ResourceCache, namespace string) []packages.Workload {
	var out []packages.Workload
	add := func(kind, ns, name string, lbls, anns map[string]string, health string) {
		// Cheap pre-filter: only Helm-suggesting rows make it to the merger.
		if lbls["helm.sh/chart"] == "" && anns["meta.helm.sh/release-name"] == "" {
			return
		}
		out = append(out, packages.Workload{
			Kind:      kind,
			Namespace: ns,
			Name:      name,
			Labels:    lbls,
			Annotations: anns,
			Health:    health,
		})
	}
	if depLister := cache.Deployments(); depLister != nil {
		var deps []*appsListResult
		if namespace != "" {
			items, _ := depLister.Deployments(namespace).List(labels.Everything())
			for _, d := range items {
				deps = append(deps, &appsListResult{
					ns: d.Namespace, name: d.Name, labels: d.Labels, anns: d.Annotations,
					health: deploymentHealth(int(d.Status.Replicas), int(d.Status.AvailableReplicas)),
				})
			}
		} else {
			items, _ := depLister.List(labels.Everything())
			for _, d := range items {
				deps = append(deps, &appsListResult{
					ns: d.Namespace, name: d.Name, labels: d.Labels, anns: d.Annotations,
					health: deploymentHealth(int(d.Status.Replicas), int(d.Status.AvailableReplicas)),
				})
			}
		}
		for _, d := range deps {
			add("Deployment", d.ns, d.name, d.labels, d.anns, d.health)
		}
	}
	if dsLister := cache.DaemonSets(); dsLister != nil {
		if namespace != "" {
			items, _ := dsLister.DaemonSets(namespace).List(labels.Everything())
			for _, d := range items {
				add("DaemonSet", d.Namespace, d.Name, d.Labels, d.Annotations,
					daemonsetHealth(int(d.Status.DesiredNumberScheduled), int(d.Status.NumberReady)))
			}
		} else {
			items, _ := dsLister.List(labels.Everything())
			for _, d := range items {
				add("DaemonSet", d.Namespace, d.Name, d.Labels, d.Annotations,
					daemonsetHealth(int(d.Status.DesiredNumberScheduled), int(d.Status.NumberReady)))
			}
		}
	}
	if ssLister := cache.StatefulSets(); ssLister != nil {
		if namespace != "" {
			items, _ := ssLister.StatefulSets(namespace).List(labels.Everything())
			for _, ss := range items {
				add("StatefulSet", ss.Namespace, ss.Name, ss.Labels, ss.Annotations,
					statefulsetHealth(int(ss.Status.Replicas), int(ss.Status.ReadyReplicas)))
			}
		} else {
			items, _ := ssLister.List(labels.Everything())
			for _, ss := range items {
				add("StatefulSet", ss.Namespace, ss.Name, ss.Labels, ss.Annotations,
					statefulsetHealth(int(ss.Status.Replicas), int(ss.Status.ReadyReplicas)))
			}
		}
	}
	return out
}

type appsListResult struct {
	ns, name string
	labels   map[string]string
	anns     map[string]string
	health   string
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
func daemonsetHealth(desired, ready int) string  { return deploymentHealth(desired, ready) }
func statefulsetHealth(desired, ready int) string { return deploymentHealth(desired, ready) }

// collectGitOpsDeclarations reads Argo Applications + Flux HelmReleases
// + Flux Kustomizations from the dynamic cache and converts each to a
// packages.Declaration. Missing CRDs (controller not installed) just
// produce no declarations; not an error. Real errors (informer
// failures) surface in the errs slice.
func collectGitOpsDeclarations(ctx context.Context, cache *k8s.ResourceCache, namespace string, errs *[]SourceError) []packages.Declaration {
	var out []packages.Declaration

	// Argo Applications (argoproj.io/Application). Argo apps are
	// always namespaced — typically in argocd or argocd-system.
	if items, err := cache.ListDynamicWithGroup(ctx, "Application", namespace, "argoproj.io"); err == nil {
		for _, item := range items {
			if d, ok := packages.ParseArgoApplication(item.Object); ok {
				out = append(out, d)
			}
		}
	} else if !isMissingCRDErr(err) {
		*errs = append(*errs, SourceError{Source: packages.SourceArgoCD, Error: err.Error()})
	}

	// Flux HelmReleases.
	if items, err := cache.ListDynamicWithGroup(ctx, "HelmRelease", namespace, "helm.toolkit.fluxcd.io"); err == nil {
		for _, item := range items {
			if d, ok := packages.ParseFluxHelmRelease(item.Object); ok {
				out = append(out, d)
			}
		}
	} else if !isMissingCRDErr(err) {
		*errs = append(*errs, SourceError{Source: packages.SourceFluxCD, Error: err.Error()})
	}

	// Flux Kustomizations.
	if items, err := cache.ListDynamicWithGroup(ctx, "Kustomization", namespace, "kustomize.toolkit.fluxcd.io"); err == nil {
		for _, item := range items {
			if d, ok := packages.ParseFluxKustomization(item.Object); ok {
				out = append(out, d)
			}
		}
	} else if !isMissingCRDErr(err) {
		// Only emit a F-source error once even if both HR + Kust failed.
		// For now keep simple: emit twice if both fail; SourcesErrored
		// is informational, dedup downstream.
		*errs = append(*errs, SourceError{Source: packages.SourceFluxCD, Error: err.Error()})
	}

	return out
}

// isMissingCRDErr matches the "unknown resource kind" error
// ListDynamicWithGroup returns when the requested CRD isn't installed.
// Cleaner than wrapping the error type: the cache returns plain
// errors.New strings.
func isMissingCRDErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unknown resource kind")
}

// userCredsForPackages — same shape as helm.userCreds but kept local
// since the helm package doesn't export it.
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

// errResourceCacheUnavailable mirrors the error other handlers return
// when the cache singleton is nil. Defined here as a package var so a
// future test can match on it.
var errResourceCacheUnavailable = packagesError("resource cache unavailable")

type packagesError string

func (e packagesError) Error() string { return string(e) }

// Prevent "imported and not used" if the encoding/json import is only
// needed transitively via writeJSON.
var _ = json.Marshal

// Convenience: the top-level Server has a global k8s.GetCache() but
// we'd rather a helper that's testable. Reserved for future use; for
// now we go direct.
var _ = log.Print
