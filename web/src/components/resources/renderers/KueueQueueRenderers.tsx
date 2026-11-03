import { useState } from 'react'
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
  return (
    <KueueQueueWorkloads
      kind={kind}
      queue={data}
      pages={pages?.length ? pages : undefined}
      loading={query.isPending}
      error={query.error ? query.error.message : null}
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
