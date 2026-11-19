// How much of one kind a workspace read, worded for people. The shape is
// integration.KindCoverage, which every workspace endpoint serves per kind.

export interface WorkspaceKindCoverage {
  state: string
  deniedNamespaces?: string[]
  uncachedNamespaces?: string[]
  allowedNamespaces?: string[]
}

const COVERAGE_LABEL: Record<string, string> = {
  denied: 'no access',
  partial: 'not read in some namespaces',
  uncached: 'not cached by Radar',
  syncing: 'still loading',
  error: 'could not be read',
}

/** Coverage states that leave some of a kind unread. */
export const INCOMPLETE_COVERAGE_STATES = new Set(['partial', 'denied', 'syncing', 'error', 'uncached'])

// The causes the server named for the namespaces it left unread. A partial or
// uncached read can name both kinds of namespace at once.
function namedCauses(cov: WorkspaceKindCoverage | undefined, uncachedNoun: string): { denied?: string; uncached?: string } {
  return {
    denied: cov?.deniedNamespaces?.length ? cov.deniedNamespaces.join(', ') : undefined,
    uncached: cov?.uncachedNamespaces?.length ? `${uncachedNoun} in ${cov.uncachedNamespaces.join(', ')}` : undefined,
  }
}

/** How much of a kind was read, in a few words ("not cached by Radar in pg"). */
export function coverageLabel(cov: WorkspaceKindCoverage | undefined): string {
  if (!cov) return 'not read by this Radar'
  const { denied, uncached } = namedCauses(cov, 'not cached by Radar')
  const named = [denied && `no access in ${denied}`, uncached].filter(Boolean)
  if (named.length > 0) return named.join('; ')
  return COVERAGE_LABEL[cov?.state ?? ''] ?? cov?.state ?? 'unknown'
}

/** Empty-state text for a collection, derived from how much of it was readable. */
export function coverageEmpty(cov: WorkspaceKindCoverage | undefined, noun: string): string {
  switch (cov?.state) {
    case 'full':
      return `No ${noun} in this scope.`
    case 'partial':
    case 'uncached': {
      const { denied, uncached } = namedCauses(cov, `Radar does not cache ${noun}`)
      const causes = [denied && `no access in ${denied}`, uncached].filter(Boolean)
      if (causes.length > 0) return `No ${noun} visible: ${causes.join('; ')}.`
      return cov.state === 'uncached' ? `Radar does not cache ${noun} in this scope.` : `No ${noun} visible. Some namespaces were not read.`
    }
    case 'denied':
      return `No access to ${noun}.`
    case 'syncing':
      return `${noun[0].toUpperCase()}${noun.slice(1)} are still loading.`
    case 'error':
      return `${noun[0].toUpperCase()}${noun.slice(1)} could not be read.`
    default:
      return `This kind is not installed.`
  }
}
