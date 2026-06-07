// Package diagnose holds vocabulary-neutral domain parsing for GitOps
// controller errors (ArgoCD operation messages + Application conditions, Flux
// condition reasons). It is the shared "what does this error mean" layer
// consumed by BOTH the per-Application insights engine (pkg/gitops/insights)
// and the cluster-wide issues engine (internal/k8s/detect_gitops.go).
//
// Design rule: this package returns PRIMITIVE parsed facts (plain strings) —
// never a UI Issue struct, an insights Remediation, or the issuesapi wire
// model. Each consumer maps these facts onto its own vocabulary. That keeps
// the package importable by everyone without an import cycle (stdlib only) and
// prevents either engine's wire shape from leaking into shared parsing.
package diagnose

import (
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// ParsedFailure carries fields extracted from an Argo operationState.message.
// Unparsed parts of the original message remain available to the caller as the
// raw error — the parser only adds structure, never replaces or hides text.
//
// Remediation is expressed as primitives (kind/target/hint strings) rather
// than a typed struct so this package stays vocabulary-neutral; the caller
// maps RemediationKind onto its own remediation type.
type ParsedFailure struct {
	Cause        string // plain-English root cause; empty if unrecognized
	AffectedKind string
	AffectedName string
	RetryCount   int
	Stuck        bool
	// RemediationKind is non-empty only for patterns with an unambiguous,
	// safe one-click fix. Today the sole value is RemediationCreateNamespace
	// ("create-namespace"), which must equal the insights/issues remediation
	// kind constant the consumers dispatch on.
	RemediationKind   string
	RemediationTarget string
	RemediationHint   string
}

// RemediationCreateNamespace mirrors the consumers' remediation-kind constant
// (insights.RemediationCreateNamespace) and the frontend literal in
// IssuesView.tsx. Kept as a literal here to avoid importing either engine; a
// divergence would silently break the one-click fix wiring, so the equality
// with insights.RemediationCreateNamespace is pinned by a test in
// pkg/gitops/insights (the only package that can import both).
const RemediationCreateNamespace = "create-namespace"

// stuckRetryThreshold is the retry count at which we stop calling a failure
// "transient" and start calling it stuck. Argo retries with backoff up to 5
// times by default; reaching that ceiling means the controller has given up
// hoping for self-recovery, which is exactly when the user needs the
// stronger visual.
const stuckRetryThreshold = 5

// Capture group: <Kind>(.<group>...)? "<name>". Examples this matches:
//
//	CustomResourceDefinition.apiextensions.k8s.io "scaledjobs.keda.sh"
//	Deployment.apps "billing"
//	Service "billing"
//
// We don't need the group; the leading kind + quoted name is what users read.
var argoAffectedRefRE = regexp.MustCompile(`([A-Z][A-Za-z0-9]+)(?:\.[A-Za-z0-9.\-]+)?\s+"([^"]+)"`)

// "(retried N times)" suffix Argo appends when its retry policy has fired.
var argoRetryRE = regexp.MustCompile(`\(retried (\d+) times?\)`)

// `namespaces "<name>" not found` — fires when the Application targets a
// namespace that doesn't exist and CreateNamespace=false. The most common
// "why won't this sync" case for new environments. Captured separately so
// the parser can populate a structured Remediation (Create namespace button)
// rather than relying on the generic affected-ref regex.
var argoMissingNamespaceRE = regexp.MustCompile(`namespaces "([^"]+)" not found`)

// Pattern table: ordered list of (matcher, plain-English cause). First match
// wins. Keep patterns specific — generic catch-alls would mask more useful
// matches. Cases below cover the failure modes operators see most: validation
// limits, admission rejection, RBAC, conflicts, registration, connectivity.
var argoErrorPatterns = []struct {
	match *regexp.Regexp
	cause string
}{
	// Missing namespace pattern: keep this first so a more specific
	// match wins over the generic "not found" message.
	{regexp.MustCompile(`namespaces "[^"]+" not found`), "The destination namespace does not exist. Create it, or enable CreateNamespace=true in the Application's syncOptions so Argo creates it on sync."},
	{regexp.MustCompile(`metadata\.annotations:\s*Too long`), "An annotation on the desired manifest exceeds Kubernetes' 256 KB metadata limit. Switch to server-side apply (Sync options → Server-side apply) or shrink the offending annotation."},
	{regexp.MustCompile(`metadata\.labels:\s*Too long`), "Labels exceed Kubernetes' 64-character-per-key limit. Shorten label keys or values."},
	// Webhook unreachable (backend down) — "failed calling webhook … no
	// endpoints available / connection refused / timeout". A very common apply
	// blocker when a webhook controller (CNPG, cert-manager, Kyverno, …) isn't
	// running. Matched BEFORE the hook patterns so it isn't shadowed, and the
	// \bhook\b word boundaries below stop "webhook" from matching the hook case.
	{regexp.MustCompile(`(?i)(?:failed calling webhook|webhook .*?).*?(?:no endpoints available|connection refused|i/o timeout|context deadline exceeded|no route to host|EOF)`), "Kubernetes couldn't reach an admission/mutating webhook to validate the apply — its backend Service has no ready endpoints or is unreachable. The webhook's controller is likely down; check that Deployment and its Service."},
	// Hook word-boundaried so it can't match the "hook" inside "webhook".
	{regexp.MustCompile(`(?i)\b(presync|postsync|sync(?:fail)?|postdelete|skipdryrun)\b.*?(?:\bhook\b|\bphase\b).*?(?:failed|error)`), "A sync hook failed. Inspect the hook resource (Job/Pod) for events and logs to see why it errored."},
	{regexp.MustCompile(`(?i)\bhook\b.*?\bfailed\b`), "A sync hook failed. Open Activity for the hook's exit reason; the failed hook resource itself usually has events that explain it."},
	{regexp.MustCompile(`admission webhook ".*?" denied the request`), "An admission webhook rejected the apply. Check the webhook's policy or its target server."},
	{regexp.MustCompile(`is forbidden:\s*User`), "RBAC denied this operation. The Argo controller's ServiceAccount lacks the required permissions."},
	{regexp.MustCompile(`already exists`), "A resource with this name already exists in the cluster. It may have been created outside of GitOps or owned by a different application."},
	{regexp.MustCompile(`no matches for kind`), "The CustomResourceDefinition for this kind isn't registered in the cluster. Install or wait for the operator that owns this CRD."},
	{regexp.MustCompile(`(?i)dial tcp.*(?:i/o timeout|connection refused|no route to host)`), "Cluster unreachable from the Argo controller. Check API server connectivity and network policies."},
	{regexp.MustCompile(`field is immutable`), "Tried to change a field Kubernetes treats as immutable. Recreate the resource (delete + reapply) or revert the change."},
	{regexp.MustCompile(`unable to recognize`), "The manifest references an API version the cluster doesn't recognize. Check apiVersion against the installed CRDs."},
	{regexp.MustCompile(`Operation cannot be fulfilled.*the object has been modified`), "The resource was modified concurrently between Argo's read and write. The next sync attempt should resolve it; investigate if it persists."},
}

// ParseArgoOperationError extracts structured facts from an Argo
// status.operationState.message. Returns a zero ParsedFailure for an empty or
// unrecognized message (the caller still surfaces the raw text).
func ParseArgoOperationError(msg string) ParsedFailure {
	if msg == "" {
		return ParsedFailure{}
	}
	out := ParsedFailure{}
	for _, p := range argoErrorPatterns {
		if p.match.MatchString(msg) {
			out.Cause = p.cause
			break
		}
	}
	if m := argoAffectedRefRE.FindStringSubmatch(msg); len(m) == 3 {
		if !isHTTPVerb(m[1]) {
			out.AffectedKind = m[1]
			out.AffectedName = m[2]
		}
	}
	if m := argoRetryRE.FindStringSubmatch(msg); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out.RetryCount = n
			out.Stuck = n >= stuckRetryThreshold
		}
	}
	// Structured remediation: only the missing-namespace pattern offers a
	// one-click fix in v1. Other patterns surface diagnosis-only via Cause.
	if m := argoMissingNamespaceRE.FindStringSubmatch(msg); len(m) == 2 {
		out.RemediationKind = RemediationCreateNamespace
		out.RemediationTarget = m[1]
		out.RemediationHint = "Creates the missing namespace and re-triggers reconciliation."
	}
	// Telemetry: when nothing matched (no Cause, no AffectedRef), log once
	// so operators can grep server logs for "operation errors that escaped
	// the recognizer" and tune the pattern table. The dedup is necessary
	// because the GitOps detail page polls every 2s during a running op —
	// a single unrecognized failure would otherwise spam the log.
	if out.Cause == "" && out.AffectedKind == "" {
		logUnrecognizedOpError(msg)
	}
	return out
}

var unrecognizedOpErrorLogged sync.Map

func logUnrecognizedOpError(msg string) {
	// Truncate at 200 chars: typical Argo error messages are short; outlier
	// stack-trace dumps would otherwise flood the log line.
	key := msg
	if len(key) > 200 {
		key = key[:200]
	}
	if _, loaded := unrecognizedOpErrorLogged.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log.Printf("[gitops/diagnose] unrecognized argo operation error (no pattern matched): %q", key)
}

func isHTTPVerb(s string) bool {
	switch strings.ToUpper(s) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

// SeverityForConditionType maps an Argo Application status.conditions[].type to
// a neutral severity token ("critical"|"warning"|"info"). Follows Argo's own
// convention: types ending in "Error" are critical, "Warning" types are
// warning, everything else is info. `recognized` is false for the info default
// so callers can elide unknown/empty condition types when they carry no
// message.
func SeverityForConditionType(condType string) (token string, recognized bool) {
	switch {
	case strings.HasSuffix(condType, "Error"):
		return "critical", true
	case strings.HasSuffix(condType, "Warning"):
		return "warning", true
	default:
		return "info", false
	}
}

// ActionForCondition returns operator-facing guidance for a known Argo
// Application condition type. Empty for unrecognized types.
func ActionForCondition(condType string) string {
	switch condType {
	case "ComparisonError":
		return "Verify the repo URL, branch/tag, and credentials. Check argocd-repo-server logs for fetch errors."
	case "InvalidSpecError":
		return "Fix the Application spec — check destination, source, and project references."
	case "SyncError":
		return "The last sync reported an error. Open the application's sync operation details for the failure, then retry."
	case "OrphanedResourceWarning":
		return "Resources exist in the destination namespace that aren't part of any application. Add to an app or label them as ignored."
	case "RepeatedResourceWarning":
		return "The same resource is declared by multiple Argo Applications. Remove the duplicate declaration."
	case "ExcludedResourceWarning":
		return "A managed resource is excluded by the Argo controller's resource.exclusions. Adjust controller config or remove the resource."
	case "SharedResourceWarning":
		return "This resource is also tracked by another Application. Move it to a single owner."
	default:
		return ""
	}
}

// ActionForFluxReason maps a Flux condition reason to operator-facing guidance.
func ActionForFluxReason(reason string) string {
	switch reason {
	case "DependencyNotReady":
		return "Inspect the dependency chain in the graph."
	case "ArtifactFailed", "ChartNotReady":
		return "Inspect the Flux source and reconcile it."
	case "BuildFailed":
		return "Check the source path and rendered manifests."
	case "HealthCheckFailed":
		return "Open unhealthy managed resources for events and status."
	case "InstallFailed", "UpgradeFailed", "TestFailed":
		return "Inspect HelmRelease conditions and controller events."
	default:
		return "Review conditions and reconcile after fixing the source of failure."
	}
}
