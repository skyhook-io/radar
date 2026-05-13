// Detect whether a Kubernetes resource is managed by a GitOps controller
// (ArgoCD or FluxCD) and return a navigable ref to its owning GitOps CR so the
// drawer can render a "Managed by <app>" affordance.
//
// Precedence (most-specific wins):
//   1. Flux HelmRelease labels
//   2. Flux Kustomize labels
//   3. Argo tracking-id annotation
//   4. Argo-specific instance label
//   5. Standard k8s instance label (best-effort; false positives possible)

// GitOpsOwnerRef is a discriminated union — the tool determines which kind is
// valid. Modeling it this way prevents callers (and consumers of the returned
// type) from constructing `{ tool: 'argo', kind: 'helmreleases' }` which would
// route to a non-existent page.
export type GitOpsOwnerRef =
  | { tool: 'argocd'; kind: 'applications'; namespace: string; name: string }
  | { tool: 'fluxcd'; kind: 'kustomizations' | 'helmreleases'; namespace: string; name: string }

// Vocabulary mirrors `pkg/gitops/tree.Tool` so the wire labels match end-to-end.
export type GitOpsOwnerTool = GitOpsOwnerRef['tool']

const ARGO_TRACKING_ID_ANNOTATION = 'argocd.argoproj.io/tracking-id'
const ARGO_INSTANCE_LABEL = 'argocd.argoproj.io/instance'
// app.kubernetes.io/instance is intentionally NOT a fallback signal here.
// It's the standard k8s recommended label and stamped by virtually every
// Helm chart in existence, not just Argo. Treating it as an Argo ownership
// hint produced false positives on plain Helm-installed resources, which
// surfaced as a misleading "Managed by <release>" chip on ordinary workload
// drawers. Argo installs that rely on this default can still be detected
// via the tracking-id annotation; the `argocd.argoproj.io/instance` label
// (above) covers Argo deployments that explicitly set their own label key.

const FLUX_KUSTOMIZE_NAME = 'kustomize.toolkit.fluxcd.io/name'
const FLUX_KUSTOMIZE_NS = 'kustomize.toolkit.fluxcd.io/namespace'
const FLUX_HELM_NAME = 'helm.toolkit.fluxcd.io/name'
const FLUX_HELM_NS = 'helm.toolkit.fluxcd.io/namespace'

export function detectGitOpsOwner(resource: unknown): GitOpsOwnerRef | null {
  if (!resource || typeof resource !== 'object') return null
  const meta = (resource as { metadata?: { labels?: Record<string, string>; annotations?: Record<string, string> } }).metadata
  const labels = meta?.labels ?? {}
  const annotations = meta?.annotations ?? {}

  const helmName = labels[FLUX_HELM_NAME]
  const helmNs = labels[FLUX_HELM_NS]
  if (helmName && helmNs) {
    return { tool: 'fluxcd', kind: 'helmreleases', namespace: helmNs, name: helmName }
  }
  const kustName = labels[FLUX_KUSTOMIZE_NAME]
  const kustNs = labels[FLUX_KUSTOMIZE_NS]
  if (kustName && kustNs) {
    return { tool: 'fluxcd', kind: 'kustomizations', namespace: kustNs, name: kustName }
  }

  const trackingID = annotations[ARGO_TRACKING_ID_ANNOTATION]
  if (trackingID) {
    const parsed = parseArgoTrackingID(trackingID)
    if (parsed) {
      return { tool: 'argocd', kind: 'applications', namespace: parsed.namespace, name: parsed.name }
    }
  }

  const instance = labels[ARGO_INSTANCE_LABEL]
  if (instance) {
    // App namespace unknown without tracking-id; emit empty so the consumer can
    // either skip the link or default to a well-known namespace.
    return { tool: 'argocd', kind: 'applications', namespace: '', name: instance }
  }

  return null
}

// Argo CD writes its tracking-id in one of two forms depending on whether
// installationID / namespaced-install is configured:
//   "<appName>:<group>/<kind>:<resourceNs>/<resourceName>"                   default
//   "<appNamespace>_<appName>:<group>/<kind>:<resourceNs>/<resourceName>"    namespaced install
// We accept both. The legacy single-name form yields an empty namespace so the
// caller can route to a "find this app" search instead of guessing.
function parseArgoTrackingID(value: string): { namespace: string; name: string } | null {
  const firstColon = value.indexOf(':')
  if (firstColon < 0) return null
  const head = value.slice(0, firstColon)
  const sep = head.indexOf('_')
  if (sep < 0) {
    return head ? { namespace: '', name: head } : null
  }
  const namespace = head.slice(0, sep)
  const name = head.slice(sep + 1)
  if (!name) return null
  return { namespace, name }
}
