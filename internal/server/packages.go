package server

import (
	"context"
	"errors"
	"fmt"
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
// walks) are not. Cache is not event-invalidated; new installs become
// visible within TTL.
const packagesCacheTTL = 60 * time.Second

// packagesCacheMaxEntries caps cache map growth. Each (user, namespace
// set) tuple gets an entry; under multi-user Hub-mode use the map would
// otherwise grow unbounded. When the cap is exceeded we evict the
// oldest entry. Var (not const) so tests can lower the cap to drive
// the eviction-at-insert path with a small fixture.
var packagesCacheMaxEntries = 256

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
	SourcesUsed    []packages.SourceCode `json:"sourcesUsed"`
	SourcesErrored []SourceError         `json:"sourcesErrored,omitempty"`
}

// SourceError carries a per-source failure.
type SourceError struct {
	Source     packages.SourceCode `json:"source"`
	StatusCode int                 `json:"statusCode,omitempty"`
	Error      string              `json:"error"`
	// AffectedNamespaces, when set, lists the namespaces this error
	// applies to. Populated when the error is scoped (e.g., a
	// per-namespace Helm RBAC denial); empty when cluster-wide.
	// Lets consumers reason about partial-result scope without
	// reverse-parsing the Error string.
	AffectedNamespaces []string `json:"affectedNamespaces,omitempty"`
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

// ErrInvalidSourceCode is returned when ListPackagesParams.Source is set
// but doesn't match one of the five known source codes (H/L/C/A/F).
// REST handlers map this to 400; MCP returns it as a tool error so the
// agent doesn't get a silent empty list and conclude "nothing installed."
var ErrInvalidSourceCode = packagesError("invalid source code (want one of H, L, C, A, F)")

// ListPackages is the public entry point shared by the REST handler
// and the MCP tool.
func ListPackages(ctx context.Context, p ListPackagesParams) (PackagesResponse, error) {
	// Validate source filter at the boundary — without this, an invalid
	// or typo'd `?source=helm` would silently return an empty list
	// (HTTP 200) and a downstream consumer would conclude "nothing
	// installed via Helm."
	if p.Source != "" {
		if !packages.SourceCode(strings.ToUpper(p.Source)).Valid() {
			return PackagesResponse{}, ErrInvalidSourceCode
		}
	}

	// Auth-restricted to no namespaces → empty response, skip cache.
	if p.Namespaces != nil && len(p.Namespaces) == 0 {
		return PackagesResponse{
			Packages:       []packages.PackageRow{},
			GeneratedAt:    time.Now(),
			SourcesUsed:    []packages.SourceCode{},
			SourcesErrored: nil,
		}, nil
	}

	cacheKey := packagesCacheKeyFor(p.User, p.Namespaces)
	packagesCacheMu.Lock()
	entry, hit := packagesCache[cacheKey]
	packagesCacheMu.Unlock()

	var rows []packages.PackageRow
	var sourceErrs []SourceError
	var generatedAt time.Time
	if hit && time.Since(entry.at) < packagesCacheTTL {
		rows = entry.rows
		sourceErrs = entry.errors
		generatedAt = entry.at
	} else {
		var err error
		rows, sourceErrs, err = computePackagesInternal(ctx, p.Namespaces, p.User, p.Groups)
		if err != nil {
			return PackagesResponse{}, err
		}
		generatedAt = time.Now()
		if len(sourceErrs) > 0 {
			log.Printf("[packages] computed with %d source errors: %+v", len(sourceErrs), sourceErrs)
		}
		packagesCacheMu.Lock()
		if len(packagesCache) >= packagesCacheMaxEntries {
			evictOldestPackagesCacheEntry()
		}
		packagesCache[cacheKey] = packagesCacheEntry{at: generatedAt, rows: rows, errors: sourceErrs}
		packagesCacheMu.Unlock()
	}

	if p.Source != "" {
		rows = filterBySource(rows, packages.SourceCode(strings.ToUpper(p.Source)))
	}
	if p.Chart != "" {
		rows = filterByChartSubstring(rows, strings.ToLower(p.Chart))
	}

	if rows == nil {
		rows = []packages.PackageRow{}
	}
	used := sourcesUsed(rows)
	if used == nil {
		used = []packages.SourceCode{}
	}
	return PackagesResponse{
		Packages:       rows,
		GeneratedAt:    generatedAt,
		SourcesUsed:    used,
		SourcesErrored: sourceErrs,
	}, nil
}

// evictOldestPackagesCacheEntry drops the entry with the earliest `at`.
// Caller must hold packagesCacheMu.
func evictOldestPackagesCacheEntry() {
	var oldestKey string
	var oldestAt time.Time
	first := true
	for k, e := range packagesCache {
		if first || e.at.Before(oldestAt) {
			oldestKey = k
			oldestAt = e.at
			first = false
		}
	}
	if !first {
		delete(packagesCache, oldestKey)
	}
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
		if errors.Is(err, errResourceCacheUnavailable) {
			s.writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if errors.Is(err, ErrInvalidSourceCode) {
			s.writeError(w, http.StatusBadRequest, err.Error())
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

	// Helm releases (source H). For a single-namespace request we ask
	// Helm directly; for nil (all namespaces) we ask cluster-wide. For a
	// multi-namespace allow-list we iterate per-namespace — asking
	// cluster-wide would require the user to have list-secrets across
	// the cluster, which limited-RBAC users typically lack.
	helmReleases, helmErrs := collectHelmReleases(namespaces, user, groups)
	src.Helm = helmReleases
	errs = append(errs, helmErrs...)

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

// collectHelmReleases reads Helm releases for the requested namespace
// scope, attributing per-source errors. RBAC denials are non-fatal —
// they surface as a SourceError with StatusCode 403 so the caller can
// distinguish "cluster has no Helm" from "user can't read Helm secrets."
//
// nil namespaces → cluster-wide (one call)
// single namespace → that namespace (one call)
// multi-namespace → one call per namespace; per-namespace forbidden
// errors are coalesced into one SourceError so a user with access to
// ns-a but not ns-b still sees ns-a's releases.
func collectHelmReleases(namespaces []string, user string, groups []string) ([]packages.HelmRelease, []SourceError) {
	// Defensive: empty (non-nil) slice means "no namespaces authorized";
	// callers should short-circuit before reaching here, but guard
	// against a future caller that forgets and would otherwise fall
	// through to a cluster-wide read.
	if namespaces != nil && len(namespaces) == 0 {
		return nil, nil
	}
	hClient := helm.GetClient()
	if hClient == nil {
		return nil, []SourceError{{
			Source: packages.SourceHelm,
			Error:  "helm client not initialized (cluster connect in progress or failed)",
		}}
	}
	scopes := []string{""}
	switch {
	case len(namespaces) == 1:
		scopes = []string{namespaces[0]}
	case len(namespaces) > 1:
		scopes = namespaces
	}
	var out []packages.HelmRelease
	var forbiddenNamespaces []string
	var otherErrNamespaces []string
	var otherErrs []error
	for _, ns := range scopes {
		releases, err := hClient.ListReleasesAsUser(ns, user, groups)
		if err == nil {
			for _, h := range releases {
				out = append(out, packages.HelmRelease{
					Name:           h.Name,
					Namespace:      h.Namespace,
					Chart:          h.Chart,
					ChartVersion:   h.ChartVersion,
					AppVersion:     h.AppVersion,
					Status:         h.Status,
					ResourceHealth: packages.Health(h.ResourceHealth),
				})
			}
			continue
		}
		if helm.IsForbiddenError(err) {
			forbiddenNamespaces = append(forbiddenNamespaces, ns)
			continue
		}
		otherErrNamespaces = append(otherErrNamespaces, ns)
		otherErrs = append(otherErrs, fmt.Errorf("ns=%q: %w", ns, err))
	}
	var errs []SourceError
	if len(forbiddenNamespaces) > 0 {
		errs = append(errs, SourceError{
			Source:             packages.SourceHelm,
			StatusCode:         http.StatusForbidden,
			Error:              "RBAC denied (helm release secrets): " + describeNamespaces(forbiddenNamespaces),
			AffectedNamespaces: namespaceList(forbiddenNamespaces),
		})
	}
	if len(otherErrs) > 0 {
		errs = append(errs, SourceError{
			Source:             packages.SourceHelm,
			Error:              errors.Join(otherErrs...).Error(),
			AffectedNamespaces: namespaceList(otherErrNamespaces),
		})
	}
	return out, errs
}

// describeNamespaces produces a human-readable label for a slice of
// namespace scopes. Empty string means "cluster-wide" — callers should
// continue to read AffectedNamespaces for structured access.
func describeNamespaces(scopes []string) string {
	labels := make([]string, 0, len(scopes))
	for _, ns := range scopes {
		if ns == "" {
			labels = append(labels, "cluster-wide")
			continue
		}
		labels = append(labels, ns)
	}
	return strings.Join(labels, ", ")
}

// namespaceList drops the "" sentinel (cluster-wide) — empty slice means
// the error applied cluster-wide; non-empty lists explicit namespaces.
func namespaceList(scopes []string) []string {
	out := make([]string, 0, len(scopes))
	for _, ns := range scopes {
		if ns != "" {
			out = append(out, ns)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// collectWorkloadInputs reads Deployments + DaemonSets + StatefulSets
// from the cache and converts them to the packages.Workload shape used
// by the merger. Only workloads with helm.sh/chart label OR
// meta.helm.sh/release-name annotation contribute. Errors from any
// lister/namespace combination are joined (with kind+ns context) so
// the caller can attribute them to source L without dropping any.
func collectWorkloadInputs(cache *k8s.ResourceCache, namespaces []string) ([]packages.Workload, error) {
	var out []packages.Workload
	var listerErrs []error
	noteErr := func(kind, ns string, err error) {
		if err == nil {
			return
		}
		nsLabel := ns
		if nsLabel == "" {
			nsLabel = "*"
		}
		listerErrs = append(listerErrs, fmt.Errorf("%s ns=%s: %w", kind, nsLabel, err))
	}
	add := func(kind, ns, name string, lbls, anns map[string]string, health packages.Health) {
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
				noteErr("Deployment", ns, err)
				for _, d := range items {
					add("Deployment", d.Namespace, d.Name, d.Labels, d.Annotations,
						deploymentHealth(int(d.Status.Replicas), int(d.Status.AvailableReplicas)))
				}
				return
			}
			items, err := depLister.Deployments(ns).List(labels.Everything())
			noteErr("Deployment", ns, err)
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
				noteErr("DaemonSet", ns, err)
				for _, d := range items {
					add("DaemonSet", d.Namespace, d.Name, d.Labels, d.Annotations,
						daemonsetHealth(int(d.Status.DesiredNumberScheduled), int(d.Status.NumberReady)))
				}
				return
			}
			items, err := dsLister.DaemonSets(ns).List(labels.Everything())
			noteErr("DaemonSet", ns, err)
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
				noteErr("StatefulSet", ns, err)
				for _, ss := range items {
					add("StatefulSet", ss.Namespace, ss.Name, ss.Labels, ss.Annotations,
						statefulsetHealth(int(ss.Status.Replicas), int(ss.Status.ReadyReplicas)))
				}
				return
			}
			items, err := ssLister.StatefulSets(ns).List(labels.Everything())
			noteErr("StatefulSet", ns, err)
			for _, ss := range items {
				add("StatefulSet", ss.Namespace, ss.Name, ss.Labels, ss.Annotations,
					statefulsetHealth(int(ss.Status.Replicas), int(ss.Status.ReadyReplicas)))
			}
		})
	}
	return out, errors.Join(listerErrs...)
}

func deploymentHealth(desired, available int) packages.Health {
	if desired == 0 {
		return packages.HealthUnknown
	}
	if available >= desired {
		return packages.HealthHealthy
	}
	if available == 0 {
		return packages.HealthUnhealthy
	}
	return packages.HealthDegraded
}
func daemonsetHealth(desired, ready int) packages.Health   { return deploymentHealth(desired, ready) }
func statefulsetHealth(desired, ready int) packages.Health { return deploymentHealth(desired, ready) }

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
			} else {
				log.Printf("[packages] failed to parse Argo Application %s/%s — skipping", item.GetNamespace(), item.GetName())
			}
		}
	} else if !isMissingCRDErr(err) {
		*errs = append(*errs, SourceError{Source: packages.SourceArgoCD, Error: err.Error()})
	}

	if items, err := cache.ListDynamicWithGroup(ctx, "HelmRelease", "", "helm.toolkit.fluxcd.io"); err == nil {
		for _, item := range items {
			if d, ok := packages.ParseFluxHelmRelease(item.Object); ok {
				out = append(out, d)
			} else {
				log.Printf("[packages] failed to parse Flux HelmRelease %s/%s — skipping", item.GetNamespace(), item.GetName())
			}
		}
	} else if !isMissingCRDErr(err) {
		*errs = append(*errs, SourceError{Source: packages.SourceFluxCD, Error: "HelmRelease: " + err.Error()})
	}

	if items, err := cache.ListDynamicWithGroup(ctx, "Kustomization", "", "kustomize.toolkit.fluxcd.io"); err == nil {
		for _, item := range items {
			if d, ok := packages.ParseFluxKustomization(item.Object); ok {
				out = append(out, d)
			} else {
				log.Printf("[packages] failed to parse Flux Kustomization %s/%s — skipping", item.GetNamespace(), item.GetName())
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

func filterBySource(rows []packages.PackageRow, src packages.SourceCode) []packages.PackageRow {
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

func sourcesUsed(rows []packages.PackageRow) []packages.SourceCode {
	seen := map[packages.SourceCode]bool{}
	for _, r := range rows {
		for _, s := range r.Sources {
			seen[s] = true
		}
	}
	out := make([]packages.SourceCode, 0, len(seen))
	for _, s := range packages.AllSourceCodes {
		if seen[s] {
			out = append(out, s)
		}
	}
	return out
}

var errResourceCacheUnavailable = packagesError("resource cache unavailable")

type packagesError string

func (e packagesError) Error() string { return string(e) }
