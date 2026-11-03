import { useEffect, useState } from 'react'
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
  const query = useKueueQueueWorkloads(
    { kind, namespace: data.metadata?.namespace, name: data.metadata?.name ?? '', uid: data.metadata?.uid },
    { membership, state },
  )
  // A cached list of an earlier queue with the same name is never shown.
  const pages = query.data?.pages.filter(page => page.queue.uid === data.metadata?.uid)
  const cursorRejected = query.isFetchNextPageError && (query.error as { data?: { error_code?: string } } | null)?.data?.error_code === 'kueue_cursor_invalid'
  const { refetch } = query
  // A cursor issued for another list (a re-created queue, another cluster
  // context) is refused: read the list again from its first page.
  useEffect(() => {
    if (cursorRejected) void refetch()
  }, [cursorRejected, refetch])
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
      upgrade={query.upgrade}
      membership={membership}
      onMembershipChange={setMembership}
      state={state}
      onStateChange={setState}
      hasMore={query.hasNextPage}
      loadingMore={query.isFetchingNextPage}
      onLoadMore={() => void query.fetchNextPage()}
      onRetry={() => void query.refetch()}
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
