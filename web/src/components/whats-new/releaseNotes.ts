import type { LucideIcon } from 'lucide-react'
import { Bot, Database, GitCompareArrows, HardDrive, Hourglass, Layers, Network, RefreshCcw, Scale, ScrollText, Search, SlidersHorizontal, Undo2 } from 'lucide-react'
import { compareVersions } from '../../utils/version'

export interface ReleaseHighlight {
  id: string
  icon: LucideIcon
  title: string
  /**
   * Shown on a grid card, so at most 150 characters. Read next to other
   * releases' highlights, so it must stand on its own.
   */
  description: string
  /**
   * Fuller copy for when this highlight leads the dialog. Only a release's
   * top highlight can ever lead (every other one scores at or below it), so
   * only that one may set it.
   */
  leadDescription?: string
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

// Read .agents/skills/whats-new/SKILL.md before authoring or regrouping notes.
// One entry per release that has something to announce. A user sees the
// entries for the releases they haven't seen yet, composed into one dialog, so
// a patch upgrade, or one that skips releases, still gets them. Every minor and
// major release must have its own entry: scripts/check-whats-new.sh refuses to
// release one without it.
export const RELEASE_NOTES: ReleaseNotes[] = [
  {
    version: 'v1.18.0',
    highlights: [
      {
        id: 'trivy-cve-search',
        icon: Search,
        title: 'Investigate CVEs across your cluster',
        description: 'Search Trivy findings by CVE, package or image, including in resource tables. MCP agents can inventory declared runtime versions.',
        leadDescription: 'With Trivy Operator installed, search globally or within report tables by CVE, package, failed check ID or image to find matching scan reports, then open a report for installed and fixed package versions. Partial searches say what could not be checked. MCP agents can also inventory runtime versions declared by running images and compare supported versions with a fixed release. Missing or unreadable declarations stay unknown; image metadata does not verify a fix.',
        importance: 8,
      },
      {
        id: 'mcp-guidance-waits-controls',
        icon: Bot,
        title: 'More capable agents through MCP',
        description: 'MCP agents get built-in tool guidance and can wait for rollouts, readiness or deletion. An optional read-only mode exposes only read tools.',
        importance: 5,
      },
      {
        id: 'investigation-change-evidence',
        icon: GitCompareArrows,
        title: 'See more of what changed',
        description: 'Investigations show more spec edits, NetworkPolicy changes, Secret rotations and stale env readers, and flag incomplete history.',
        importance: 5,
      },
      {
        id: 'workload-blockers',
        icon: Network,
        title: 'Better issue detection and warnings',
        description: 'Issues catch more network, storage and scheduling problems, add diagnostic context, and flag checks that could not complete.',
        importance: 5,
        path: '/issues',
        cta: 'Open Issues',
      },
      {
        id: 'log-viewer-improvements',
        icon: ScrollText,
        title: 'Log viewer improvements',
        description: 'Keep your place in fast-moving logs, select text without auto-scroll, and wait for containers to start with clearer status messages.',
        importance: 4,
      },
    ],
    improvements: [
      'Resource tables remember your column order for each kind',
      'Open terminals and browse files in native sidecars',
      'Node drains keep results visible and name remaining Pods',
      'Audits recognize shared Pod CPU and memory budgets',
      'Helm upgrades need a confirmed source; find public sources on ArtifactHub',
      'Prometheus reconnects automatically when it appears',
      'Argo CD views confirm when a local resource is missing',
    ],
  },
  {
    version: 'v1.17.0',
    highlights: [
      {
        id: 'cloudnative-pg',
        icon: Database,
        title: 'CloudNativePG, end to end',
        description: 'See which Postgres cluster needs attention and why, whether its backups would restore, and switch over, fence or restore with a review first.',
        leadDescription: 'New views for CloudNativePG show which Postgres cluster needs attention and why, what each instance is doing, and whether its backups would actually let you restore. Create, restore, back up, switch over, fence and hibernate from Radar: you review exactly what will be written, then Radar follows the operation until it sees the outcome.',
        importance: 9,
        path: '/cnpg',
        cta: 'Open CloudNativePG',
      },
      {
        id: 'live-traffic-large-clusters',
        icon: Network,
        title: 'Live Traffic on large Cilium clusters',
        description: 'Traffic is drawn per workload and stays fast on big clusters. Focus on one workload, or get a connection list when the map is too big.',
        importance: 7,
        path: '/traffic',
        cta: 'Open Live Traffic',
      },
      {
        id: 'gitops-revert-warnings',
        icon: Undo2,
        title: 'Know before an edit gets reverted',
        description: 'Before Radar edits an object owned by Argo CD, Flux or Helm, it tells you whether the owner will undo the change.',
        importance: 6,
      },
      {
        id: 'stuck-deletion-issues',
        icon: Hourglass,
        title: 'Issues for stuck deletions',
        description: "Helm uninstalls and custom resources stuck deleting raise issues, and so do volumes that can't attach to their node's machine type.",
        importance: 6,
        path: '/issues',
        cta: 'Open Issues',
      },
      {
        id: 'audit-unused-storage',
        icon: HardDrive,
        title: 'Cluster Audit finds unused storage',
        description: 'Claims nothing uses, claims Pending for over a day, and Released PersistentVolumes, with their size and storage class.',
        // Below v1.16's headline at its 0.75 weight, so an upgrade from v1.15
        // still shows that card.
        importance: 4,
        path: '/checks',
        cta: 'Open Checks',
      },
    ],
    improvements: [
      // The grid fills row by row: pair lines of similar length.
      "Capacity explains why a node pool isn't scaling down",
      'Nodes being removed show as removed, not failed',
      'Helm actions on Flux releases change the right objects',
      'Flux Sync with source matches flux reconcile --with-source',
      'Topology links Services only to the Pods they select',
      'Namespaces stuck Terminating show what holds them',
      'Filters stay put when you switch kinds or sections',
      'Skip an audit check per resource with an annotation',
    ],
  },
  {
    version: 'v1.16.0',
    highlights: [
      {
        id: 'restart-loop-issues',
        icon: RefreshCcw,
        title: 'One steady issue per crash loop',
        description: 'A crash-looping container stays one issue until it recovers, with probe failures and exits as evidence. No more repeat alerts.',
        leadDescription: 'A restarting container is one issue until it recovers, instead of a new one on every check, so no more repeat alerts and investigations. Probe failures, the last exit and how many replicas are affected sit on it as evidence, and severity follows impact.',
        importance: 6,
        path: '/issues',
        cta: 'Open Issues',
      },
      {
        id: 'log-viewer',
        icon: ScrollText,
        title: 'Log viewer enhancements',
        description: 'Levels from the logger itself, stack traces kept with their errors, Hide matching, and a chip for lines without a level.',
        importance: 5,
      },
      {
        id: 'per-cluster-settings',
        icon: SlidersHorizontal,
        title: 'Separate settings for each cluster',
        description: 'Running Radar locally, each kubeconfig context keeps its own Metrics, Argo CD and Cost connections.',
        importance: 4,
      },
    ],
    improvements: [
      // The grid fills row by row: pair lines of similar length.
      "A workload's Timeline shows only its own history",
      "Timeline's Problems filter no longer hides crash loops",
      'Live Traffic figures corrected for Cilium and Istio',
      'Saved namespace picks survive a failing namespace list',
      'Terminal tabs show and keep the context they opened for',
      'YAML review flags a resource that changed after review',
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
        description: 'A clear verdict and the story of what broke, with the evidence placed where the agent cites it. Runs on OpenCode too.',
        leadDescription: 'A clear verdict and the story of what broke, with the charts, logs and config behind it placed where the agent cites them. Runs on OpenCode too, including AWS Bedrock.',
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
  return catalog.find(notes => notes.version === normalized || releaseLine(notes.version) === normalized)
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
// A fuller set of useful improvements can merit an announcement even without
// a single release-defining feature.
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
