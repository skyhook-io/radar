export type KindCoverageState = 'full' | 'partial' | 'denied' | 'notInstalled' | 'syncing' | 'uncached' | 'error'
export interface KindCoverage {
  state: KindCoverageState
  deniedNamespaces?: string[]
  uncachedNamespaces?: string[]
  allowedNamespaces?: string[]
}
