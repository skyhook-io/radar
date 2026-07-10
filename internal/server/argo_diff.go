package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/pkg/argoapi"
	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
)

// lastAppliedAnnotation embeds a full JSON copy of the applied manifest,
// including Secret data. It is stripped from both sides before a Secret diff
// is serialized so redaction can't be bypassed through the annotation.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

const (
	redactedUnchanged = "<redacted:unchanged>"
	redactedChanged   = "<redacted:changed>"
)

// argoResourceDiffResponse is the desired-vs-live diff for a single resource
// managed by an Argo CD Application. Desired/Live are YAML manifest strings
// (empty when that side doesn't exist); FieldEntries is a scannable per-field
// summary derived from the same manifests. For Secrets the values are masked
// and Redacted is true.
type argoResourceDiffResponse struct {
	Source       string                      `json:"source"`
	Desired      string                      `json:"desired"`
	Live         string                      `json:"live"`
	FieldEntries []gitopsinsights.DriftEntry `json:"fieldEntries"`
	Redacted     bool                        `json:"redacted"`
	Hook         bool                        `json:"hook"`
}

// handleArgoResourceDiff serves the desired-vs-live manifest diff for one
// resource in an Argo CD Application's managed set, sourced from the Argo CD
// API server (not the local cache) so it reflects Argo's own normalized and
// predicted states.
//
// Authorization is a dual gate, both enforced BEFORE any Argo API call:
//  1. the Application root — the caller must have access to the Application's
//     namespace, mirroring how the insights handler authorizes a GitOps root.
//  2. the target resource — the per-user preflight (namespace access +
//     cluster-scoped/Secret SARs) that gates every single-resource read.
//
// Secret data is structurally redacted (see redactSecretManifest) before the
// manifests are diffed or serialized; there is no un-redact option.
func (s *Server) handleArgoResourceDiff(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	appNamespace := normalizeNamespaceParam(chi.URLParam(r, "namespace"))
	appName := chi.URLParam(r, "name")

	group := r.URL.Query().Get("group")
	kind := r.URL.Query().Get("kind")
	resourceNamespace := r.URL.Query().Get("resourceNamespace")
	resourceName := r.URL.Query().Get("resourceName")
	if kind == "" || resourceName == "" {
		s.writeError(w, http.StatusBadRequest, "kind and resourceName query parameters are required")
		return
	}
	// An Argo Application always lives in a namespace; an empty segment would
	// skip gate 1 below, so require it explicitly rather than silently degrade
	// to a single gate.
	if appNamespace == "" {
		s.writeError(w, http.StatusBadRequest, "application namespace is required")
		return
	}

	// Gate 1: the Application root. A caller who can't see the Application's
	// namespace is denied here, before any upstream fetch. Matches the
	// namespace-access check parseGitOpsRequest runs for /api/gitops/insights.
	if noNamespaceAccess(s.getUserNamespaces(r, []string{appNamespace})) {
		s.writeError(w, http.StatusForbidden, fmt.Sprintf("no access to namespace %q", appNamespace))
		return
	}

	// Gate 2: the target resource. The same preflight the resource drawer's GET
	// uses — namespace access plus per-kind SARs for cluster-scoped kinds and
	// Secrets. A user who can see the Application but lacks `get` on the target
	// (a Secret they can't read) is denied here, still before any Argo call.
	if status, msg, ok := s.preflightResourceGet(r, normalizeKind(kind), resourceNamespace, resourceName, group); !ok {
		s.writeError(w, status, msg)
		return
	}

	if !argocd.IsConfigured() {
		s.writeError(w, http.StatusServiceUnavailable, "Argo CD integration is not connected")
		return
	}

	// App-level query (no per-resource filter) so it shares the manager's 15s
	// cache; per-resource filters would bypass the cache. ManagedResourcesCached
	// connects on demand (synchronous probe) when the background reconnect hasn't
	// landed yet, so the first diff after a restart works. We filter in-process.
	items, err := argocd.ManagedResourcesCached(r.Context(), argoapi.ManagedResourcesQuery{
		AppName:      appName,
		AppNamespace: appNamespace,
	})
	if err != nil {
		s.writeArgoDiffError(w, appNamespace, appName, err)
		return
	}

	item, found := findManagedResource(items, group, kind, resourceNamespace, resourceName)
	if !found {
		s.writeError(w, http.StatusNotFound, "resource is not in the Application's managed set")
		return
	}

	// Prefer Argo's server-side-dry-run prediction and normalized live state;
	// fall back to the raw target/live manifests when those aren't populated.
	desiredObj := parseArgoManifest(desiredState(item))
	liveObj := parseArgoManifest(liveState(item))

	// Argo's states retain managedFields and the last-applied annotation.
	// managedFields is pure apply-machinery noise that dwarfs the actual
	// manifest in the side-by-side view; last-applied embeds a full manifest
	// copy. Radar-wide policy (pkg/k8score StripUnstructuredFields) is that
	// neither reaches outward payloads. Stripped from BOTH sides, so the
	// removal is diff-neutral.
	for _, obj := range []map[string]any{desiredObj, liveObj} {
		stripManifestNoise(obj)
	}

	redacted := false
	if isCoreSecret(kind, group) {
		redactSecretManifest(desiredObj, liveObj)
		redacted = true
	}

	s.writeJSON(w, argoResourceDiffResponse{
		Source:       "argocd-api",
		Desired:      manifestToYAML(desiredObj),
		Live:         manifestToYAML(liveObj),
		FieldEntries: gitopsinsights.DiffObjects(desiredObj, liveObj),
		Redacted:     redacted,
		Hook:         item.Hook,
	})
}

func desiredState(item argoapi.ResourceDiff) string {
	if item.PredictedLiveState != "" {
		return item.PredictedLiveState
	}
	return item.TargetState
}

func liveState(item argoapi.ResourceDiff) string {
	if item.NormalizedLiveState != "" {
		return item.NormalizedLiveState
	}
	return item.LiveState
}

// findManagedResource locates the managed-set entry matching the requested
// resource identity. Kind/group are compared case-insensitively (Argo reports
// PascalCase kinds and lowercase groups); namespace/name are exact.
func findManagedResource(items []argoapi.ResourceDiff, group, kind, namespace, name string) (argoapi.ResourceDiff, bool) {
	for _, it := range items {
		if strings.EqualFold(it.Kind, kind) &&
			strings.EqualFold(it.Group, group) &&
			it.Namespace == namespace &&
			it.Name == name {
			return it, true
		}
	}
	return argoapi.ResourceDiff{}, false
}

func isCoreSecret(kind, group string) bool {
	return strings.EqualFold(kind, "Secret") && group == ""
}

// redactSecretManifest masks every Secret value on BOTH manifests in place so
// no raw secret material can reach the response. It is fail-CLOSED: any field
// that could hold secret material is masked regardless of its shape, and
// annotation values are masked too (a Secret's annotations can carry token
// material — service-account tokens, bootstrap tokens, the last-applied dump).
// Key names stay visible so the operator still sees what changed.
func redactSecretManifest(desired, live map[string]any) {
	// data + stringData are the canonical Secret payload fields; binaryData is
	// masked too as defense-in-depth — it isn't valid on a core Secret, but a
	// hand-crafted manifest could still stash bytes there, and masking a field
	// that should always be empty costs nothing.
	for _, field := range []string{"data", "stringData", "binaryData"} {
		maskSecretField(desired, live, field)
	}
	maskSecretAnnotations(desired, live)
}

// maskSecretField masks a data/stringData field. When the field is a map, it
// masks per key with changed/unchanged markers; when it is any OTHER shape (a
// scalar or list — a malformed manifest, but still potential secret material),
// it replaces the whole field with the changed marker. The field is never
// emitted with a real value.
func maskSecretField(desired, live map[string]any, field string) {
	dRaw, dPresent := desired[field]
	lRaw, lPresent := live[field]
	if !dPresent && !lPresent {
		return
	}
	desiredData, dIsMap := dRaw.(map[string]any)
	liveData, lIsMap := lRaw.(map[string]any)
	if (dPresent && !dIsMap) || (lPresent && !lIsMap) {
		// A non-map data field is malformed; mask the whole thing rather than
		// risk emitting a scalar secret.
		if dPresent {
			desired[field] = redactedChanged
		}
		if lPresent {
			live[field] = redactedChanged
		}
		return
	}
	for k := range unionMapKeys(desiredData, liveData) {
		dv, dok := desiredData[k]
		lv, lok := liveData[k]
		marker := redactedChanged
		if dok && lok && reflect.DeepEqual(dv, lv) {
			marker = redactedUnchanged
		}
		if dok {
			desiredData[k] = marker
		}
		if lok {
			liveData[k] = marker
		}
	}
}

// maskSecretAnnotations masks every annotation VALUE on both manifests (keys
// preserved) so nothing sensitive stashed in an annotation — including the
// last-applied dump that embeds the full data — survives into the response.
func maskSecretAnnotations(desired, live map[string]any) {
	dAnno := nestedAnnotations(desired)
	lAnno := nestedAnnotations(live)
	for k := range unionMapKeys(dAnno, lAnno) {
		dv, dok := dAnno[k]
		lv, lok := lAnno[k]
		marker := redactedChanged
		if dok && lok && reflect.DeepEqual(dv, lv) {
			marker = redactedUnchanged
		}
		if dok {
			dAnno[k] = marker
		}
		if lok {
			lAnno[k] = marker
		}
	}
}

// nestedAnnotations returns metadata.annotations as a map, or nil when absent
// or the wrong shape (a malformed non-map annotations block is dropped entirely
// by the caller path — DiffObjects only descends metadata.labels/annotations
// maps, so a scalar there is never rendered).
func nestedAnnotations(obj map[string]any) map[string]any {
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		return nil
	}
	if anno, ok := meta["annotations"].(map[string]any); ok {
		return anno
	}
	// Non-map annotations on a Secret are malformed and can't be safely
	// masked key-wise; drop the whole block.
	delete(meta, "annotations")
	return nil
}

func unionMapKeys(a, b map[string]any) map[string]struct{} {
	keys := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}
	return keys
}

func deleteLastAppliedAnnotation(obj map[string]any) {
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		return
	}
	annotations, ok := meta["annotations"].(map[string]any)
	if !ok {
		return
	}
	delete(annotations, lastAppliedAnnotation)
	if len(annotations) == 0 {
		delete(meta, "annotations")
	}
}

func stripManifestNoise(obj map[string]any) {
	deleteLastAppliedAnnotation(obj)
	if meta, ok := obj["metadata"].(map[string]any); ok {
		delete(meta, "managedFields")
	}
	// Never declared intent — DiffObjects skips it for field entries, and
	// rendering it in the manifest pair only pads the diff view.
	delete(obj, "status")
}

// parseArgoManifest decodes an Argo CD *State JSON manifest. An empty input
// (the resource doesn't exist on that side) yields an empty map. A parse
// failure is logged and also yields an empty map — for Secrets in particular,
// falling back to the raw string could leak unredacted data, so we never do.
func parseArgoManifest(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		log.Printf("[argo] resource-diff: failed to parse manifest JSON: %v", err)
		return map[string]any{}
	}
	if obj == nil {
		return map[string]any{}
	}
	return obj
}

// manifestToYAML renders a manifest as YAML, returning "" for an empty object
// so the frontend can treat that side as "does not exist" rather than "{}".
func manifestToYAML(obj map[string]any) string {
	if len(obj) == 0 {
		return ""
	}
	b, err := yaml.Marshal(obj)
	if err != nil {
		log.Printf("[argo] resource-diff: failed to marshal manifest to YAML: %v", err)
		return ""
	}
	return string(b)
}

// writeArgoDiffError maps managed-resources fetch failures to HTTP status
// codes. Token problems (either the manager's auth verification or an
// upstream 401/403) are 403 with a re-auth hint; unreachable is 503;
// everything else is a logged 500.
func (s *Server) writeArgoDiffError(w http.ResponseWriter, namespace, name string, err error) {
	switch {
	case errors.Is(err, argocd.ErrTokenInvalid) || errors.Is(err, argoapi.ErrUnauthorized):
		s.writeError(w, http.StatusForbidden, "Argo CD rejected the configured token; re-authenticate the integration in Settings.")
	case errors.Is(err, argocd.ErrUnreachable):
		s.writeError(w, http.StatusServiceUnavailable, "Argo CD API server is unreachable.")
	default:
		// The upstream error can wrap Argo's raw response body (proxy headers,
		// a render error containing Secret data). Keep it in the server log
		// only; the client gets a generic message.
		log.Printf("[argo] resource-diff for %s/%s failed: %s", sanitizeForLog(namespace), sanitizeForLog(name), sanitizeForLog(err.Error()))
		s.writeError(w, http.StatusBadGateway, "Failed to fetch the diff from the Argo CD API server.")
	}
}

// enrichArgoRepoHealth appends an Issue when Argo CD reports a failed connection
// to a repository this Application sources from — a common, otherwise-invisible
// cause of stuck syncs. Non-blocking: RepositoriesCached never hits the network
// on the insights hot path. An unmatched repo is treated as unknown (not
// asserted healthy) so we only ever surface a failure Argo actually reports.
// Argo's raw connection error is carried in RawMessage for diagnosis (same
// audience already sees the repo URL + repo errors on the Application itself).
func (s *Server) enrichArgoRepoHealth(root *unstructured.Unstructured, insight *gitopsinsights.Insight) {
	urls := appRepoURLs(root)
	if len(urls) == 0 {
		return
	}
	repos := argocd.RepositoriesCached()
	if len(repos) == 0 {
		return
	}
	project, _, _ := unstructured.NestedString(root.Object, "spec", "project")
	if project == "" {
		project = "default"
	}
	seen := make(map[string]bool, len(urls))
	for _, u := range urls {
		norm := normalizeRepoURL(u)
		if seen[norm] {
			continue
		}
		seen[norm] = true
		rp := matchRepo(repos, norm, project)
		if rp == nil || !strings.EqualFold(rp.ConnectionState.Status, "Failed") {
			continue
		}
		// A failed repo connection is the CAUSE of any ComparisonError Argo
		// raised for this app: it couldn't load the desired state because it
		// couldn't read the repo. Rather than stack a second, lower-severity
		// "RepoUnreachable" alert beside that ComparisonError — one root cause
		// rendered as two problems — fold the repo diagnosis into the existing
		// issue so the page points at the one thing that matters. Only when
		// there is no ComparisonError (repo degraded but the app is still
		// Synced from Argo's cache) do we surface a standalone warning.
		if enrichComparisonErrorWithRepo(insight, u, rp.ConnectionState.Message) {
			continue
		}
		insight.Issues = append(insight.Issues, gitopsinsights.Issue{
			Severity:   gitopsinsights.SeverityWarning,
			Scope:      gitopsinsights.ScopeCondition,
			Reason:     "RepoUnreachable",
			Message:    fmt.Sprintf("Argo CD can't reach the source repository %s", u),
			RawMessage: rp.ConnectionState.Message,
			Action:     "Check the repository's credentials and network access in Argo CD (Settings → Repositories).",
		})
	}
}

// enrichComparisonErrorWithRepo folds a failed repo-connection diagnosis into
// an existing ComparisonError issue — the symptom Argo raised when it couldn't
// load the desired state — naming the specific repo and giving the fix action
// while keeping the issue's critical severity. Returns true when it found and
// enriched one, so the caller skips adding a duplicate standalone
// RepoUnreachable. The Argo condition's own full text stays in RawMessage; the
// repo connection error only fills it when the condition didn't carry detail.
func enrichComparisonErrorWithRepo(insight *gitopsinsights.Insight, repoURL, connErr string) bool {
	for i := range insight.Issues {
		if insight.Issues[i].Reason != "ComparisonError" {
			continue
		}
		insight.Issues[i].Message = fmt.Sprintf("Argo CD can't reach the source repository %s, so it can't compare against Git — sync status is unavailable for all resources.", repoURL)
		insight.Issues[i].Action = "Check the repository's credentials and network access in Argo CD (Settings → Repositories)."
		if insight.Issues[i].RawMessage == "" {
			insight.Issues[i].RawMessage = connErr
		}
		return true
	}
	return false
}

// matchRepo finds the repository entry for a normalized URL, preferring an entry
// scoped to the app's project over a global (empty-project) one — mirroring how
// Argo disambiguates the same URL registered under different projects, so we
// never read project B's health for project A's app.
func matchRepo(repos []argoapi.Repository, normURL, project string) *argoapi.Repository {
	var global *argoapi.Repository
	for i := range repos {
		if normalizeRepoURL(repos[i].Repo) != normURL {
			continue
		}
		if repos[i].Project == project {
			return &repos[i]
		}
		if repos[i].Project == "" {
			global = &repos[i]
		}
	}
	return global
}

// appRepoURLs collects the Git source URLs of an Argo CD Application, covering
// single-source (spec.source), multi-source (spec.sources[]), and source-hydrator
// (spec.sourceHydrator.drySource) apps.
func appRepoURLs(root *unstructured.Unstructured) []string {
	if root == nil {
		return nil
	}
	var urls []string
	if u, ok, _ := unstructured.NestedString(root.Object, "spec", "source", "repoURL"); ok && u != "" {
		urls = append(urls, u)
	}
	if sources, ok, _ := unstructured.NestedSlice(root.Object, "spec", "sources"); ok {
		for _, s := range sources {
			if m, ok := s.(map[string]any); ok {
				if u, _ := m["repoURL"].(string); u != "" {
					urls = append(urls, u)
				}
			}
		}
	}
	if u, ok, _ := unstructured.NestedString(root.Object, "spec", "sourceHydrator", "drySource", "repoURL"); ok && u != "" {
		urls = append(urls, u)
	}
	return urls
}

// normalizeRepoURL canonicalizes a Git repo URL for matching an app's source
// against Argo's stored repositories, approximating Argo's own URL equivalence:
// drop the scheme and any user@ prefix, convert scp-style "host:path" to
// "host/path", and strip a trailing slash / ".git". This makes cross-protocol
// forms of the same repo compare equal; anything still unmatched fails safe.
func normalizeRepoURL(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:] // strip scheme (https://, ssh://, git://)
	}
	// Strip a user@ that precedes the host (the '@' before the first '/').
	if at := strings.Index(u, "@"); at >= 0 {
		if slash := strings.Index(u, "/"); slash < 0 || at < slash {
			u = u[at+1:]
		}
	}
	// scp-style host:path → host/path, but leave host:port (':' before a digit).
	if colon := strings.Index(u, ":"); colon >= 0 {
		rest := u[colon+1:]
		if rest == "" || rest[0] < '0' || rest[0] > '9' {
			u = u[:colon] + "/" + rest
		}
	}
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	return u
}

// handleArgoRevisionMetadata returns the Git commit metadata (author, message,
// date, signature) for a deployed revision of an Argo CD Application. Unlike the
// resource diff, this is app-scoped Git data — not a K8s resource — so it gates
// on Application-namespace access only (no per-resource SAR) and needs no Secret
// redaction. Available only when the Argo CD API integration is connected.
func (s *Server) handleArgoRevisionMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	appNamespace := normalizeNamespaceParam(chi.URLParam(r, "namespace"))
	appName := chi.URLParam(r, "name")
	revision := r.URL.Query().Get("revision")
	if appNamespace == "" {
		s.writeError(w, http.StatusBadRequest, "application namespace is required")
		return
	}
	if revision == "" {
		s.writeError(w, http.StatusBadRequest, "revision query parameter is required")
		return
	}

	// Gate: the caller must be able to see the Application's namespace, matching
	// the resource-diff and insights handlers.
	if noNamespaceAccess(s.getUserNamespaces(r, []string{appNamespace})) {
		s.writeError(w, http.StatusForbidden, fmt.Sprintf("no access to namespace %q", appNamespace))
		return
	}

	if !argocd.IsConfigured() {
		s.writeError(w, http.StatusServiceUnavailable, "Argo CD integration is not connected")
		return
	}

	meta, err := argocd.RevisionMetadataCached(r.Context(), argoapi.RevisionMetadataQuery{
		AppName:      appName,
		AppNamespace: appNamespace,
		Project:      r.URL.Query().Get("project"),
		SourceIndex:  r.URL.Query().Get("sourceIndex"),
		Revision:     revision,
	})
	if err != nil {
		switch {
		case errors.Is(err, argocd.ErrTokenInvalid) || errors.Is(err, argoapi.ErrUnauthorized):
			s.writeError(w, http.StatusForbidden, "Argo CD rejected the configured token; re-authenticate the integration in Settings.")
		case errors.Is(err, argocd.ErrUnreachable):
			s.writeError(w, http.StatusServiceUnavailable, "Argo CD API server is unreachable.")
		case errors.Is(err, argoapi.ErrNotFound):
			s.writeError(w, http.StatusNotFound, "Argo CD has no metadata for this revision.")
		default:
			log.Printf("[argo] revision-metadata for %s/%s failed: %s", sanitizeForLog(appNamespace), sanitizeForLog(appName), sanitizeForLog(err.Error()))
			s.writeError(w, http.StatusBadGateway, "Failed to fetch revision metadata from the Argo CD API server.")
		}
		return
	}
	s.writeJSON(w, meta)
}
