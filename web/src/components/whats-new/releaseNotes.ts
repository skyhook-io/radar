import type { LucideIcon } from 'lucide-react'
import { Bot, GitCompareArrows, Layers, Network, Scale } from 'lucide-react'
import { compareVersions } from '../../utils/version'

export interface ReleaseHighlight {
  id: string
  icon: LucideIcon
  title: string
  description: string
  /** In-app route the highlight's call to action opens. */
  path?: string
  cta?: string
  /** Icon tile color, so the cards read apart at a glance. The lead card always uses the accent. */
  tone?: HighlightTone
}

export type HighlightTone = 'violet' | 'teal' | 'emerald' | 'indigo'

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
        description: 'A clear verdict and the story of what broke, with the charts, logs and config behind it placed where the agent cites them. Now also runs on OpenCode, including AWS Bedrock.',
      },
      {
        id: 'gitops-manifest-diff',
        icon: GitCompareArrows,
        title: 'Full manifest diffs for Argo CD resources',
        description: 'Compare the Git-rendered and live manifest of a drifted Argo CD resource, side by side or unified.',
        tone: 'violet',
        path: '/gitops',
        cta: 'Open GitOps',
      },
      {
        id: 'rightsizing-risks-first',
        icon: Scale,
        title: 'Rightsizing and cost data in MCP',
        description: 'Agents get new optimized tools. Risks (OOM, limit conflicts and throttling) lead before cost-saving, in the UI as well.',
        tone: 'emerald',
        path: '/cost/rightsizing',
        cta: 'Open Rightsizing',
      },
      {
        id: 'batch-ai-workloads',
        icon: Layers,
        title: 'Deeper batch + AI/ML workloads support',
        description: 'Dedicated pages for Kueue, JobSet and Ray resources, and Job and JobSet details now show why a queued job has no Pods.',
        tone: 'teal',
      },
      {
        id: 'rollout-traffic-topology',
        icon: Network,
        title: 'Canary and blue-green in Topology',
        description: 'See canary and stable Pods, which Services route to each side, and the canary traffic weight.',
        tone: 'indigo',
        path: '/topology',
        cta: 'Open Topology',
      },
    ],
    improvements: [
      // The grid fills row by row: pair lines of similar length.
      'Resource tables remember your sort for each kind',
      'Install Helm charts from OCI registries',
      'Checks flag expired and expiring TLS certificates',
      'Radar Desktop keeps your preferences across restarts',
      'Issues show where a failing Pod differs from its template',
      'PVC details say why usage data is unavailable',
      'Issues catch Strimzi connector and admission webhook failures',
      'Bind the web UI to a specific IP with --listen-address',
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

/** The release line a catalog entry covers, e.g. v1.15 for v1.15.0: its patches show the same notes. */
export function releaseLine(version: string): string {
  return version.replace(/^(v\d+\.\d+)\.\d+$/, '$1')
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
