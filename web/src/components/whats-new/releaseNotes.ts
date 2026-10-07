import type { LucideIcon } from 'lucide-react'
import { Bot, GitCompareArrows, History, Layers, Network, Scale, ScrollText, SlidersHorizontal } from 'lucide-react'
import { compareVersions } from '../../utils/version'

export interface ReleaseHighlight {
  id: string
  icon: LucideIcon
  title: string
  /** Read next to other releases' highlights, so it must stand on its own. */
  description: string
  /**
   * 1-10, comparable across releases: it decides which highlights a user who
   * skipped releases sees, and whether the dialog opens by itself.
   *   9-10  a new capability that defines the release
   *   7-8   a solid feature a typical user would go try now
   *   4-6   a useful improvement
   *   1-3   polish
   * Most highlights are not 9s. When every card is a 7, every release
   * interrupts the user, which is what the score exists to prevent.
   */
  importance: number
  /** In-app route the highlight's call to action opens. */
  path?: string
  cta?: string
}

export interface ReleaseNotes {
  version: string
  highlights: ReleaseHighlight[]
  improvements: string[]
}

export const CHANGELOG_URL = 'https://radarhq.io/changelog'

// One entry per release that has something to announce. A user sees the
// entries for the releases they haven't seen yet, composed into one dialog, so
// a patch upgrade, or one that skips releases, still gets them. Every minor and
// major release must have its own entry: scripts/check-whats-new.sh refuses to
// release one without it.
export const RELEASE_NOTES: ReleaseNotes[] = [
  {
    version: 'v1.16.0',
    highlights: [
      {
        id: 'workload-timeline',
        icon: History,
        title: "Each workload's own history on its Timeline",
        description: "A workload's Timeline covers its own full history, past runs, and the traffic and config in front of it, without its neighbors. Routine activity folds behind a toggle; problems always show.",
        importance: 6,
        path: '/timeline',
        cta: 'Open Timeline',
      },
      {
        id: 'log-levels',
        icon: ScrollText,
        title: 'Log levels you can filter on',
        description: 'Levels come from what the logger wrote, filtering to errors keeps their stack traces, and Hide matching drops noisy lines.',
        importance: 5,
      },
      {
        id: 'per-cluster-settings',
        icon: SlidersHorizontal,
        title: 'Integration settings per cluster',
        description: 'Running Radar locally, each kubeconfig context keeps its own Metrics, Argo CD and Cost connections.',
        importance: 4,
      },
    ],
    improvements: [
      // The grid fills row by row: pair lines of similar length.
      'Live Traffic figures corrected for Cilium and Istio',
      'A restart loop stays one issue instead of flapping',
      'Terminal tabs show and keep the context they opened for',
      'YAML review says when the resource changed since',
      'Helm chart meets the Pod Security restricted profile',
      'Helm chart extraArgs passes through any Radar flag',
    ],
  },
  {
    version: 'v1.15.0',
    highlights: [
      {
        id: 'ai-investigations',
        icon: Bot,
        title: 'AI investigations that show their work',
        // No link: the investigations page redirects home when this run
        // mode can't host local agents.
        description: 'A clear verdict and the story of what broke, with the charts, logs and config behind it placed where the agent cites them. Runs on OpenCode too, including AWS Bedrock.',
        importance: 9,
      },
      {
        id: 'gitops-manifest-diff',
        icon: GitCompareArrows,
        title: 'Full manifest diffs for Argo CD resources',
        description: 'Compare the Git-rendered and live manifest of a drifted Argo CD resource, side by side or unified.',
        importance: 8,
        path: '/gitops',
        cta: 'Open GitOps',
      },
      {
        id: 'rightsizing-risks-first',
        icon: Scale,
        title: 'Rightsizing and cost data in MCP',
        description: 'Agents get new optimized tools. Risks (OOM, limit conflicts and throttling) lead before cost-saving, in the UI as well.',
        importance: 7,
        path: '/cost/rightsizing',
        cta: 'Open Rightsizing',
      },
      {
        id: 'batch-ai-workloads',
        icon: Layers,
        title: 'Deeper batch + AI/ML workloads support',
        description: 'Dedicated pages for Kueue, JobSet and Ray resources, and Job and JobSet details now show why a queued job has no Pods.',
        importance: 7,
      },
      {
        id: 'rollout-traffic-topology',
        icon: Network,
        title: 'Canary and blue-green in Topology',
        description: 'See canary and stable Pods, which Services route to each side, and the canary traffic weight.',
        importance: 7,
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

/** The entries at or below `version`, newest first: what that version's user has access to. */
function releasesUpTo(version: string, catalog: ReleaseNotes[]): ReleaseNotes[] {
  return catalog
    .filter(notes => (compareVersions(notes.version, version) ?? 1) <= 0)
    .sort((a, b) => compareVersions(b.version, a.version) ?? 0)
}

/** The newest notes for a release at or below `version`. */
export function latestReleaseNotesFor(
  version: string | undefined,
  catalog: ReleaseNotes[] = RELEASE_NOTES,
): ReleaseNotes | undefined {
  if (!version) return undefined
  return releasesUpTo(version, catalog)[0]
}

/**
 * The entries a user hasn't seen, newest first: those after `lastSeen`, up to
 * the running version. A fresh install has nothing to compare against and gets
 * none; an install that predates the seen record, or whose record isn't a
 * version, gets them all.
 */
export function unreadReleases(
  currentVersion: string,
  lastSeen: string | null,
  priorInstall: boolean,
  catalog: ReleaseNotes[] = RELEASE_NOTES,
): ReleaseNotes[] {
  const available = releasesUpTo(currentVersion, catalog)
  if (lastSeen === null) return priorInstall ? available : []
  return available.filter(notes => (compareVersions(notes.version, lastSeen) ?? 1) > 0)
}

// How much a highlight counts by how far back its release is. A user who
// skipped releases sees the best of them, weighted toward the newest; older
// releases than this list covers are left to the changelog.
export const RELEASE_WEIGHTS = [1, 0.75, 0.5]
export const MAX_CARDS = 5
export const MAX_LINES = 8
// The dialog opens by itself only when the cards it shows add up to this.
// A release of mostly 4-6s stays behind the unread dot; one with a few solid
// features, or a skipped release with them, opens it.
export const AUTO_OPEN_SCORE = 25

export interface ComposedHighlight extends ReleaseHighlight {
  version: string
  score: number
}

export interface ComposedLine {
  version: string
  text: string
}

export interface ComposedNotes {
  /** The releases shown, newest first. */
  versions: string[]
  /** The first one leads the dialog at full width. */
  highlights: ComposedHighlight[]
  lines: ComposedLine[]
  /** Sum of the shown highlights' scores. */
  score: number
  /** False when older unseen releases were left out. */
  complete: boolean
}

/** One dialog from releases ordered newest first. */
export function composeReleaseNotes(releases: ReleaseNotes[]): ComposedNotes {
  const shown = releases.slice(0, RELEASE_WEIGHTS.length)
  // Built newest release first, in authored order, so the stable sort breaks
  // ties the same way.
  const ranked = shown
    .flatMap((notes, i) => notes.highlights.map(h => ({ ...h, version: notes.version, score: h.importance * RELEASE_WEIGHTS[i] })))
    .sort((a, b) => b.score - a.score)
  const highlights = ranked.slice(0, MAX_CARDS)
  const lines: ComposedLine[] = [
    ...ranked.slice(MAX_CARDS).map(h => ({ version: h.version, text: h.title })),
    ...shown.flatMap(notes => notes.improvements.map(text => ({ version: notes.version, text }))),
  ].slice(0, MAX_LINES)
  return {
    versions: shown.map(notes => notes.version),
    highlights,
    lines,
    score: highlights.reduce((sum, h) => sum + h.score, 0),
    complete: shown.length === releases.length,
  }
}
