// Detect whether a Kubernetes resource is managed by a GitOps controller
// (ArgoCD or FluxCD) based on the standard labels/annotations each writes onto
// the objects it owns. Returns a navigable ref to the owning GitOps CR so the
// drawer can render a "Managed by <app>" affordance.
//
// Detection precedence: Argo's tracking-id annotation is the most authoritative
// when present (encodes both the app namespace and name), followed by Flux's
// kind-specific labels, then Argo's bare instance label as a last resort.

export type GitOpsOwnerTool = 'argo' | 'flux'

export interface GitOpsOwnerRef {
  tool: GitOpsOwnerTool
  kind: 'applications' | 'kustomizations' | 'helmreleases'
  namespace: string
  name: string
}

// ArgoCD uses different conventions in different versions. tracking-id is the
// newer canonical form: "<appNamespace>_<appName>:<group>/<kind>:<resourceNs>/<resourceName>".
// The older instance label is just "<appName>" with no namespace — we treat the
// app namespace as unknown in that case and let the caller decide (usually
// "argocd" is a safe default but emitting empty lets the caller fall back).
const ARGO_TRACKING_ID_ANNOTATION = 'argocd.argoproj.io/tracking-id'
const ARGO_INSTANCE_LABEL = 'argocd.argoproj.io/instance'
// Argo's instance label is configurable via application.instanceLabelKey in
// argocd-cmd-params-cm; many installs (including the default in pre-2.5 Argo
// and several popular tutorials) leave it as the standard k8s recommended
// label. We treat this as a lower-confidence fallback: it can be set by any
// chart, so false positives are possible, but the cost of a false positive is
// just a "not found" on the GitOps detail page — not destructive.
const HELM_INSTANCE_LABEL = 'app.kubernetes.io/instance'

// Flux writes kind-specific labels on every object it reconciles. The Kustomize
// controller and the Helm controller use different label prefixes; an object
// can carry both (a HelmRelease deployed via a parent Kustomization), in which
// case we prefer the most-direct owner — the HelmRelease.
const FLUX_KUSTOMIZE_NAME = 'kustomize.toolkit.fluxcd.io/name'
const FLUX_KUSTOMIZE_NS = 'kustomize.toolkit.fluxcd.io/namespace'
const FLUX_HELM_NAME = 'helm.toolkit.fluxcd.io/name'
const FLUX_HELM_NS = 'helm.toolkit.fluxcd.io/namespace'

export function detectGitOpsOwner(resource: unknown): GitOpsOwnerRef | null {
  if (!resource || typeof resource !== 'object') return null
  const meta = (resource as { metadata?: { labels?: Record<string, string>; annotations?: Record<string, string> } }).metadata
  const labels = meta?.labels ?? {}
  const annotations = meta?.annotations ?? {}

  // Prefer the most-direct Flux owner (HelmRelease beats parent Kustomization).
  const helmName = labels[FLUX_HELM_NAME]
  const helmNs = labels[FLUX_HELM_NS]
  if (helmName && helmNs) {
    return { tool: 'flux', kind: 'helmreleases', namespace: helmNs, name: helmName }
  }
  const kustName = labels[FLUX_KUSTOMIZE_NAME]
  const kustNs = labels[FLUX_KUSTOMIZE_NS]
  if (kustName && kustNs) {
    return { tool: 'flux', kind: 'kustomizations', namespace: kustNs, name: kustName }
  }

  // Argo tracking-id encodes the app's namespace; parse it before falling back
  // to the bare instance label.
  const trackingID = annotations[ARGO_TRACKING_ID_ANNOTATION]
  if (trackingID) {
    const parsed = parseArgoTrackingID(trackingID)
    if (parsed) {
      return { tool: 'argo', kind: 'applications', namespace: parsed.namespace, name: parsed.name }
    }
  }

  const instance = labels[ARGO_INSTANCE_LABEL] || labels[HELM_INSTANCE_LABEL]
  if (instance) {
    // App namespace unknown without tracking-id; emit empty so the consumer can
    // either skip the link or default to a well-known namespace. Most installs
    // run Argo in "argocd", but newer multi-tenant setups deploy apps in any
    // namespace — guessing would route to the wrong page.
    return { tool: 'argo', kind: 'applications', namespace: '', name: instance }
  }

  return null
}

// tracking-id format (Argo CD ≥ 2.5):
//   "<appNamespace>_<appName>:<group>/<kind>:<resourceNs>/<resourceName>"
// Legacy fallback (older Argo):
//   "<appName>:<group>/<kind>:<resourceNs>/<resourceName>" — no app namespace
function parseArgoTrackingID(value: string): { namespace: string; name: string } | null {
  const firstColon = value.indexOf(':')
  if (firstColon < 0) return null
  const head = value.slice(0, firstColon)
  const sep = head.indexOf('_')
  if (sep < 0) {
    // Legacy single-name form — no namespace component.
    return head ? { namespace: '', name: head } : null
  }
  const namespace = head.slice(0, sep)
  const name = head.slice(sep + 1)
  if (!name) return null
  return { namespace, name }
}
