import type { LucideIcon } from 'lucide-react'
import { Bot, GitCompareArrows, Layers, Network, Scale, ShieldAlert } from 'lucide-react'
import { compareVersions } from '../../utils/version'

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

// One entry per release that has something to announce. A version without its
// own entry shows the newest entry at or below it, so a patch upgrade (or one
// that skips the release with notes) still gets them. Every minor and major
// release must have its own entry: scripts/check-whats-new.sh refuses to
// release one without it.
export const RELEASE_NOTES: ReleaseNotes[] = [
  {
    version: 'v1.15.0',
    releaseUrl: 'https://github.com/skyhook-io/radar/releases/tag/v1.15.0',
    highlights: [
      {
        id: 'ai-investigations',
        icon: Bot,
        title: 'AI investigations that show their work',
        // No link: the investigations workspace redirects home when this run
        // mode can't host local agents.
        description: 'A clear verdict, then the story of what broke, with Radar\'s own evidence placed where the agent cites it: the metrics chart around the change, the log lines, the Helm revision or RBAC rule behind it. Now also runs on your OpenCode setup, including AWS Bedrock.',
      },
      {
        id: 'gitops-manifest-diff',
        icon: GitCompareArrows,
        title: 'Full manifest diffs for Argo CD resources',
        description: 'Expand any drifted resource to compare the Git-rendered manifest with the live one, side by side or unified. Resources an app deploys to another cluster say which cluster they live on.',
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
        description: 'OOM signals, limit conflicts and throttling lead the list, partial evidence no longer hides the guidance that is known, and AI assistants can query it over MCP.',
        path: '/cost/rightsizing',
        cta: 'Open Rightsizing',
      },
      {
        id: 'rollout-traffic-topology',
        icon: Network,
        title: 'Canary and blue-green Rollouts in Topology',
        description: 'See which Pods are canary or stable (active or preview for blue-green), which Services route to each side, and the canary traffic weight.',
        path: '/topology',
        cta: 'Open Topology',
      },
      {
        id: 'kueue-jobset',
        icon: Layers,
        title: 'See why a queued JobSet has no Pods',
        description: 'Follow a JobSet to its Kueue Workload, queues and admission checks, and drill into each role and member Job. Ray clusters and services get their own detail pages too.',
      },
    ],
    improvements: [
      'Resource tables remember your sort for each resource kind',
      'Install Helm charts from OCI registries',
      'Issues show where a failing Pod differs from its owner template',
      'Strimzi connector task failures and admission webhook call failures surface in Issues',
      'PVC details say why usage data is unavailable',
      'Bind the web UI to a specific IP with --listen-address',
      'The install script honors a custom INSTALL_DIR',
      'AWS Load Balancer Controller Ingress backends resolve through action annotations',
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

/** The newest notes for a release at or below `version` — what that version's user has access to. */
export function latestReleaseNotesFor(
  version: string | undefined,
  catalog: ReleaseNotes[] = RELEASE_NOTES,
): ReleaseNotes | undefined {
  if (!version) return undefined
  let latest: ReleaseNotes | undefined
  for (const notes of catalog) {
    const atOrBelow = compareVersions(notes.version, version)
    if (atOrBelow === null || atOrBelow > 0) continue
    if (!latest || (compareVersions(notes.version, latest.version) ?? 0) > 0) latest = notes
  }
  return latest
}
