import { LookupFailureNote } from '@skyhook-io/k8s-ui/components/resources/renderers/LookupFailureNote'
import { VeleroRunMessages, type VeleroRunMessagesFetch } from '@skyhook-io/k8s-ui/components/resources/renderers/VeleroRunMessages'
import { getBackupErrors, getBackupWarnings, getRestoreErrors, getRestoreWarnings } from '@skyhook-io/k8s-ui/components/resources/resource-utils-velero'
import { useVeleroRunMessages } from '../../api/policy'

type RunKind = 'backups' | 'restores'

// The phases Velero's issues are raised for (veleroPartialPhases and Failed in
// internal/issues/source_velero.go).
const FAILED_PHASES = new Set(['Failed', 'PartiallyFailed', 'FinalizingPartiallyFailed', 'WaitingForPluginOperationsPartiallyFailed'])

const ISSUE_REASONS: Record<string, RunKind> = {
  BackupFailed: 'backups',
  BackupPartiallyFailed: 'backups',
  RestoreFailed: 'restores',
  RestorePartiallyFailed: 'restores',
}

/**
 * Where a run's messages go: inside its issue's "What's wrong" when the drawer
 * shows that issue, since they are what is wrong; in Status otherwise.
 */
export function veleroMessagesInIssue(data: any, issuesShown: boolean): boolean {
  return issuesShown && FAILED_PHASES.has(data?.status?.phase ?? '')
}

// Nothing is fetched until the operator asks: reading the messages makes
// Velero create a DownloadRequest and pulls an object out of storage.
export function useVeleroRunMessagesFetch(kind: RunKind, data: any): VeleroRunMessagesFetch {
  const namespace = data?.metadata?.namespace ?? ''
  const name = data?.metadata?.name ?? ''
  const fetcher = useVeleroRunMessages(kind, namespace, name)
  return {
    messages: fetcher.data,
    loading: fetcher.isPending,
    lookupNote: fetcher.error ? <LookupFailureNote errors={[fetcher.error]} what="the messages behind these counts" /> : undefined,
    onFetch: namespace && name ? () => fetcher.mutate() : undefined,
  }
}

/** A run's errors and warnings behind its counts, fetched on request. */
export function RunMessages({ kind, resource }: { kind: RunKind; resource: any }) {
  const fetch = useVeleroRunMessagesFetch(kind, resource)
  const errors = kind === 'backups' ? getBackupErrors(resource) : getRestoreErrors(resource)
  const warnings = kind === 'backups' ? getBackupWarnings(resource) : getRestoreWarnings(resource)
  return <VeleroRunMessages {...fetch} errors={errors} warnings={warnings} />
}

/** A failed run's messages, under its issue in the drawer. */
export function VeleroRunIssueMessages({ issue, resource }: { issue: { kind?: string; reason?: string }; resource: any }) {
  const kind = ISSUE_REASONS[issue.reason ?? '']
  if (!kind || !resource || !veleroMessagesInIssue(resource, true)) return null
  return <RunMessages kind={kind} resource={resource} />
}
