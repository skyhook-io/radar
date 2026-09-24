import type { LucideIcon } from 'lucide-react'
import { Bot, GitCompareArrows, Layers, Network, Scale, ShieldAlert } from 'lucide-react'

export interface ReleaseHighlight {
  id: string
  icon: LucideIcon
  title: string
  description: string
  /** In-app route the highlight's call to action opens. */
  path?: string
  cta?: string
}

export interface ReleaseNotes {
  version: string
  /** The first highlight leads the dialog at full width. */
  highlights: ReleaseHighlight[]
  improvements: string[]
  releaseUrl: string
}

// One entry per released version, added in that release's PR. A version with
// no entry shows neither the dialog nor the Home link.
export const RELEASE_NOTES: ReleaseNotes[] = [
  {
    version: 'v1.15.0',
    releaseUrl: 'https://github.com/skyhook-io/radar/releases',
    highlights: [
      {
        id: 'rollout-traffic-topology',
        icon: Network,
        title: 'Canary and blue-green traffic in Topology',
        description: 'Argo Rollouts now show which Pods are canary or stable, which Services route to each side, and how much traffic each one gets.',
        path: '/topology',
        cta: 'Open Topology',
      },
      {
        id: 'gitops-manifest-diff',
        icon: GitCompareArrows,
        title: 'Full manifest diffs for Argo CD resources',
        description: 'Expand any drifted resource to compare the Git-rendered manifest with the live one, side by side or unified.',
        path: '/gitops',
        cta: 'Open GitOps',
      },
      {
        id: 'tls-expiry-audit',
        icon: ShieldAlert,
        title: 'Expiring TLS certificates in Checks',
        description: 'Certificates in TLS Secrets that expire within 30 days now land in the Checks queue, high severity under 7 days.',
        path: '/checks',
        cta: 'Open Checks',
      },
      {
        id: 'rightsizing-risks-first',
        icon: Scale,
        title: 'Rightsizing puts risk before savings',
        description: 'OOM signals, limit conflicts and throttling lead the list, and partial evidence no longer hides the guidance that is known.',
        path: '/cost/rightsizing',
        cta: 'Open Rightsizing',
      },
      {
        id: 'kueue-jobset',
        icon: Layers,
        title: 'Kueue admission and JobSet detail',
        description: 'See why a queued JobSet has no Pods: admission checks, quota and queue conditions, plus drilldown into each role and member Job.',
      },
      {
        id: 'opencode-investigations',
        icon: Bot,
        title: 'Investigate with OpenCode',
        // No link: the investigations workspace redirects home when this run
        // mode can't host local agents.
        description: 'Run investigations through your existing OpenCode setup, including AWS Bedrock, alongside Claude Code and Codex.',
      },
    ],
    improvements: [
      'AI assistants can query cost and rightsizing data over MCP',
      'Install Helm charts from OCI registries',
      'Issues compare a failing Pod with its owner template when they differ',
      'Strimzi connector task failures and admission webhook call failures surface in Issues',
      'PVC details say when usage data is unavailable instead of showing zero',
      'Bind the web UI to a specific IP with --listen-address',
      'The install script honors a custom INSTALL_DIR',
      'AWS Load Balancer Controller Ingress backends resolve through action annotations',
      'More credential patterns are redacted from AI context',
    ],
  },
]

export function releaseNotesFor(
  version: string | undefined,
  catalog: ReleaseNotes[] = RELEASE_NOTES,
): ReleaseNotes | undefined {
  if (!version) return undefined
  const normalized = version.startsWith('v') ? version : `v${version}`
  return catalog.find(notes => notes.version === normalized)
}
