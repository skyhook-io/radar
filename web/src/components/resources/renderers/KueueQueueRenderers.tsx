import { useEffect, useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import {
  ClusterQueueRenderer as BaseClusterQueueRenderer,
  LocalQueueRenderer as BaseLocalQueueRenderer,
  clusterQueueRelatedNames,
  type ClusterQueueReads,
} from '@skyhook-io/k8s-ui/components/resources/renderers/KueueQueueRenderers'
import { getClusterQueueCohort, isKueueQueueResource } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kueue'
import { needsNamespaceLabels } from '@skyhook-io/k8s-ui/components/kueue/eligibility'
import { localQueueStatusWithParent } from '@skyhook-io/k8s-ui/components/kueue/queue-status'
import type { RelatedRead } from '@skyhook-io/k8s-ui/components/kueue/related-read'
import { KUEUE_GROUP } from '@skyhook-io/k8s-ui/components/kueue/constants'
import { relatedReadKey, useRelatedObjects, useRelatedReads } from '../../kueue/useRelatedReads'

import { KueueQueueWorkloads } from '@skyhook-io/k8s-ui/components/resources/renderers/KueueQueueWorkloads'
import type { KueueQueueMembership } from '@skyhook-io/k8s-ui/types/kueue-queues'
import { useKueueQueueWorkloads } from '../../../api/client'

type Navigate = (ref: ResourceRef) => void

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



// Keyed by UID so moving the drawer to another queue starts its reads (and their mount time) afresh.
export function LocalQueueRenderer({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  return <LocalQueueReads key={data.metadata?.uid} data={data} onNavigate={onNavigate} />
}

function LocalQueueReads({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  const parentName: string = data.spec?.clusterQueue ?? ''
  const namespace: string = data.metadata?.namespace ?? ''
  const subjectKey = relatedReadKey(data, [parentName], ['Active'])
  const parentRead = useRelatedObjects('clusterqueues', KUEUE_GROUP, undefined, [parentName], !!parentName, subjectKey).byName[parentName]
  const clusterQueue = parentName ? parentRead : undefined
  const needsLabels = clusterQueue?.state === 'found' && needsNamespaceLabels(clusterQueue.object?.spec?.namespaceSelector)
  const namespaceState = JSON.stringify([subjectKey, needsLabels, clusterQueue?.state === 'found' ? clusterQueue.object?.spec?.namespaceSelector : null])
  const [namespaceRead] = useRelatedReads([{ kind: 'namespaces', name: namespace }], needsLabels && !!namespace, namespaceState)
  return <BaseLocalQueueRenderer data={data} onNavigate={onNavigate} reads={{ clusterQueue, namespace: needsLabels ? namespaceRead : undefined }} workloads={<QueueWorkloads kind="LocalQueue" data={data} onNavigate={onNavigate} />} />
}

export function ClusterQueueRenderer({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  return <ClusterQueueReads key={data.metadata?.uid} data={data} onNavigate={onNavigate} />
}

function ClusterQueueReads({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  const names = useMemo(() => clusterQueueRelatedNames(data), [data])
  const cohort = getClusterQueueCohort(data)
  const subjectKey = relatedReadKey(data, [...names.flavors, ...names.admissionChecks], ['Active'])
  const flavors = useRelatedObjects('resourceflavors', KUEUE_GROUP, undefined, names.flavors, names.flavors.length > 0, subjectKey)
  const checks = useRelatedObjects('admissionchecks', KUEUE_GROUP, undefined, names.admissionChecks, names.admissionChecks.length > 0, subjectKey)
  const queues = useRelatedObjects('clusterqueues', KUEUE_GROUP, undefined, [], cohort !== '-', relatedReadKey(data, [cohort], ['Active']))
  const members: RelatedRead<string[]> | undefined = cohort === '-' ? undefined
    : queues.all.state !== 'found' ? queues.all as RelatedRead<string[]>
      : {
          state: 'found',
          object: queues.all.object
            .filter(queue => queue?.metadata?.name !== data.metadata?.name && getClusterQueueCohort(queue) === cohort)
            .map(queue => String(queue.metadata.name))
            .sort(),
        }
  const reads: ClusterQueueReads = {
    flavors: flavors.byName,
    admissionChecks: checks.byName,
    cohortMembers: members,
    cohortMembersCompleteness: queues.completeness,
  }
  return <BaseClusterQueueRenderer data={data} onNavigate={onNavigate} reads={reads} workloads={<QueueWorkloads kind="ClusterQueue" data={data} onNavigate={onNavigate} />} />
}

/** The header badge: the LocalQueue's own report, toned like its detail by the ClusterQueue it names. */
export function LocalQueueStatusBadge({ data }: { data: any }) {
  return <LocalQueueBadgeReads key={data?.metadata?.uid} data={data} />
}

function LocalQueueBadgeReads({ data }: { data: any }) {
  const parentName: string = data?.spec?.clusterQueue ?? ''
  const supported = isKueueQueueResource(data)
  const parentRead = useRelatedObjects('clusterqueues', KUEUE_GROUP, undefined, [parentName], supported && !!parentName, relatedReadKey(data, [parentName], ['Active'])).byName[parentName]
  const parent = parentName ? parentRead : undefined
  const status = localQueueStatusWithParent(data, supported ? parent : undefined)
  return <span className={`badge whitespace-nowrap ${status.color}`}>{status.text}</span>
}
