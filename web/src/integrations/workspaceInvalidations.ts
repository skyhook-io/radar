// Which workspace queries a live resource change makes stale. App.tsx collects
// the due entries while it batches events and invalidates each once per batch.
export interface WorkspaceEvent { kind: string; group?: string; operation?: 'add' | 'update' | 'delete' }

export interface WorkspaceInvalidation {
  id: string
  matches: (event: WorkspaceEvent) => boolean
  queryKey: readonly unknown[]
}

export const workspaceInvalidations: WorkspaceInvalidation[] = [
  { id: 'cnpg', matches: e => !!e.group?.endsWith('.cnpg.io'), queryKey: ['cnpg', 'workspace'] },
  // A running pod going away moves the pipeline's counts. A new pod is Pending
  // (the counts take running pods only) and status churn would rescan every
  // pod per batch; the mounted page polls for both.
  { id: 'logging', matches: e => e.group === 'logging.banzaicloud.io' || (e.kind === 'Pod' && !e.group && e.operation === 'delete'), queryKey: ['logging', 'pipeline'] },
]

export function pendingInvalidations(events: WorkspaceEvent[]): WorkspaceInvalidation[] {
  return workspaceInvalidations.filter(w => events.some(e => w.matches(e)))
}
