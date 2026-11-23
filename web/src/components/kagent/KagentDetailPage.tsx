import { Badge, PaneLoader } from '@skyhook-io/k8s-ui'
import type { WorkloadExtraTab } from '@skyhook-io/k8s-ui'
import { History, MessagesSquare } from 'lucide-react'
import type { SelectedResource } from '../../types'
import { useCapabilities, useRadarFeature, useResource } from '../../api/client'
import { WorkloadView } from '../workload/WorkloadView'
import { ScreenEmptyState } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { useContextPin } from '../substrate/contextPin'
import { KAGENT_GROUP, KAGENT_KINDS, type KagentTarget } from './routes'
import { readKagent } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kagent'
import { KagentAgentRenderer as AgentSpec } from '@skyhook-io/k8s-ui/components/resources/renderers/KagentAgentRenderer'
import { KagentRevisions, KagentSessionsTab } from './KagentRuntimeTabs'

export function KagentDetailPage({
  target,
  onOpenResource,
}: {
  target: KagentTarget
  onOpenResource: (resource: SelectedResource) => void
}) {
  const pin = useContextPin()
  const navigate = useWorkspaceNavigate()
  const feature = useRadarFeature('kagentWorkspace')
  const { isPending: checking } = useCapabilities()
  const agent = useResource<any>('agents', target.namespace, target.name, KAGENT_GROUP, { enabled: !pin.mismatch && target.plural === 'agents' && feature.support === 'supported' })
  const reading = agent.data ? readKagent(agent.data) : undefined
  const latestChange = reading?.state === 'ready' ? 'prepared' : reading?.state === 'stale' ? 'notProcessed' : reading?.state
  const spec = KAGENT_KINDS[target.plural]
  if (feature.support === 'unknown' && checking) return <PaneLoader label="Checking kagent workspace support…" className="flex-1" />
  if (pin.mismatch)
    return (
      <ScreenEmptyState
        icon={History}
        title={`${target.name} is not in ${pin.active}`}
        detail={`This link names ${pin.pinned}. Radar does not open a same-named object from another cluster.`}
        action={
          <button type="button" onClick={() => navigate('/kagent')} className="btn-secondary mt-3 px-3 py-1.5 text-sm">
            Go to kagent overview
          </button>
        }
      />
    )
  const tabs: WorkloadExtraTab[] | undefined =
    target.plural === 'agents' && feature.support === 'supported'
      ? [
          {
            id: 'sessions',
            label: 'Sessions',
            after: 'overview',
            icon: <MessagesSquare className="h-4 w-4" />,
            render: () => <KagentSessionsTab agent={target} />,
          },
          {
            id: 'revisions',
            label: 'Revisions',
            after: 'overview',
            icon: <History className="h-4 w-4" />,
            render: () => <KagentRevisions agent={target} latestChange={latestChange} />,
          },
        ]
      : undefined
  return (
    <WorkloadView
      key={`${target.plural}/${target.namespace}/${target.name}/${pin.active}`}
      kind={target.plural}
      group={KAGENT_GROUP}
      namespace={target.namespace}
      name={target.name}
      expanded
      hideKindBadge
      hideBackButton
      namespaceDisplay={<Badge kind="Namespace">{target.namespace}</Badge>}
      inlineBadges
      onBack={() => navigate(spec.listPath)}
      onNavigateToResource={onOpenResource}
      extraTabs={tabs}
      specTab={target.plural === 'agents' && feature.support === 'supported' ? { render: () => <KagentAgentSpec target={target} /> } : undefined}
      tabOrder={['overview', 'sessions', 'revisions', 'spec', 'timeline', 'events', 'yaml']}
      titlePrefix={
        <nav aria-label="Location" className="flex items-center gap-1.5 text-sm text-theme-text-tertiary">
          <button type="button" onClick={() => navigate('/kagent')} className="hover:underline">
            kagent
          </button>
          <span>/</span>
          <button type="button" onClick={() => navigate(spec.listPath)} className="hover:underline">
            {spec.label}
          </button>
          <span>/</span>
        </nav>
      }
    />
  )
}

function KagentAgentSpec({ target }: { target: KagentTarget }) {
  const query = useResource<any>('agents', target.namespace, target.name, KAGENT_GROUP)
  if (query.isLoading) return <PaneLoader label="Reading Agent spec…" />
  if (!query.data) return <p className="p-4 text-sm text-theme-text-secondary">{query.error instanceof Error ? query.error.message : 'Agent spec could not be read.'}</p>
  return <AgentSpec data={query.data} specOnly />
}
