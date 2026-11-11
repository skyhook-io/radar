const ROW_KINDS: Record<string, string> = {
  Application: 'argoproj.io',
  ApplicationSet: 'argoproj.io',
  Kustomization: 'kustomize.toolkit.fluxcd.io',
  HelmRelease: 'helm.toolkit.fluxcd.io',
}

/** Whether a resource change can alter a row of the GitOps table (its rows query is ['gitops-rows-main', …]). */
export function changesGitOpsRow(kind: string, group: string | undefined): boolean {
  return ROW_KINDS[kind] !== undefined && ROW_KINDS[kind] === group
}
