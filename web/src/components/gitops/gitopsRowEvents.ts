import { isGitOpsCountGroup } from '@skyhook-io/k8s-ui'

const FLUX_SOURCES = 'source.toolkit.fluxcd.io'

// Row kinds, plus the Flux sources whose URLs the rows show.
const ROW_KINDS: Record<string, string> = {
  Application: 'argoproj.io',
  ApplicationSet: 'argoproj.io',
  Kustomization: 'kustomize.toolkit.fluxcd.io',
  HelmRelease: 'helm.toolkit.fluxcd.io',
  GitRepository: FLUX_SOURCES,
  HelmRepository: FLUX_SOURCES,
  OCIRepository: FLUX_SOURCES,
  Bucket: FLUX_SOURCES,
}

/** Whether a resource change can alter a row of the GitOps table (its rows query is ['gitops-rows-main', …]). */
export function changesGitOpsRow(kind: string, group: string | undefined): boolean {
  return ROW_KINDS[kind] !== undefined && ROW_KINDS[kind] === group
}

/**
 * What a resource change makes stale in the GitOps table: its rows, and on an
 * add or delete its totals. A status update never changes a total.
 */
export function gitopsTableChange(kind: string, group: string | undefined, operation: string): { rows: boolean; counts: boolean } {
  return {
    rows: changesGitOpsRow(kind, group),
    counts: (operation === 'add' || operation === 'delete') && isGitOpsCountGroup(group),
  }
}
