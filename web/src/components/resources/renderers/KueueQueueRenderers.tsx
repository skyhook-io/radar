import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import {
  ClusterQueueRenderer as BaseClusterQueueRenderer,
  LocalQueueRenderer as BaseLocalQueueRenderer,
} from '@skyhook-io/k8s-ui/components/resources/renderers/KueueQueueRenderers'
import { KueueQueueWorkloads } from '@skyhook-io/k8s-ui/components/resources/renderers/KueueQueueWorkloads'
import type { KueueQueueMembership } from '@skyhook-io/k8s-ui/types/kueue-queues'
import { useKueueQueueWorkloads } from '../../../api/client'

interface Props {
  data: any
  onNavigate?: (ref: ResourceRef) => void
}

function QueueWorkloads({ kind, data, onNavigate }: Props & { kind: 'ClusterQueue' | 'LocalQueue' }) {
  const [membership, setMembership] = useState<KueueQueueMembership | undefined>()
  const [state, setState] = useState<string | undefined>()
  const [restarted, setRestarted] = useState<string | null>(null)
  const query = useKueueQueueWorkloads(
    { kind, namespace: data.metadata?.namespace, name: data.metadata?.name ?? '', uid: data.metadata?.uid },
    { membership, state },
  )
  // A cached list of an earlier queue with the same name is never shown.
  const pages = query.data?.pages.filter(page => page.queue.uid === data.metadata?.uid)
  const cursorRejected = (query.error as { data?: { error_code?: string } } | null)?.data?.error_code === 'kueue_cursor_invalid'
  const { refetch } = query
  const queryClient = useQueryClient()
  // Refresh reads the list again from its first page, dropping the pages
  // loaded since: replaying them all would reread the queue once per page.
  const restart = () => void queryClient.resetQueries({ queryKey: ['kueue-queue-workloads', kind, data.metadata?.namespace ?? '', data.metadata?.name ?? '', data.metadata?.uid ?? ''] })
  // Reopening a queue whose list was paged reads it again from the first
  // page: the loaded pages aren't polled, so they could be stale.
  useEffect(() => {
    if ((query.data?.pages.length ?? 0) > 1) restart()
    // eslint-disable-next-line react-hooks/exhaustive-deps -- on mount only
  }, [])
  // A cursor issued for another list (a re-created queue, another cluster
  // context) is refused: read the list again from its first page.
  useEffect(() => {
    if (cursorRejected) setRestarted('restarted')
    if (cursorRejected) void queryClient.resetQueries({ queryKey: ['kueue-queue-workloads', kind, data.metadata?.namespace ?? '', data.metadata?.name ?? '', data.metadata?.uid ?? ''] })
  }, [cursorRejected, queryClient, kind, data.metadata?.namespace, data.metadata?.name, data.metadata?.uid])
  return (
    <KueueQueueWorkloads
      kind={kind}
      queue={data}
      pages={pages?.length ? pages : undefined}
      // A response for a re-created queue's predecessor (or successor) is
      // still being replaced: keep reading rather than claim nothing was read.
      loading={query.isPending || (!!query.data && !pages?.length)}
      error={query.error ? query.error.message : null}
      nextPageError={query.isFetchNextPageError ? query.error?.message ?? null : null}
      stale={query.isPlaceholderData}
      notice={restarted ? 'The list restarted from its first page.' : null}
      upgrade={query.upgrade}
      membership={membership}
      onMembershipChange={next => { setRestarted(null); setMembership(next) }}
      state={state}
      onStateChange={next => { setRestarted(null); setState(next) }}
      hasMore={query.hasNextPage}
      loadingMore={query.isFetchingNextPage}
      onLoadMore={() => { setRestarted(null); void query.fetchNextPage() }}
      onRetry={() => { setRestarted(null); if (pages && pages.length > 1) restart(); else void refetch() }}
      onNavigate={onNavigate}
    />
  )
}

export function ClusterQueueRenderer({ data, onNavigate }: Props) {
  return <BaseClusterQueueRenderer data={data} onNavigate={onNavigate} workloads={<QueueWorkloads kind="ClusterQueue" data={data} onNavigate={onNavigate} />} />
}

export function LocalQueueRenderer({ data, onNavigate }: Props) {
  return <BaseLocalQueueRenderer data={data} onNavigate={onNavigate} workloads={<QueueWorkloads kind="LocalQueue" data={data} onNavigate={onNavigate} />} />
}
