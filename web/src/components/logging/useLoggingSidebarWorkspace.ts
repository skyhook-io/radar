import { useMemo } from 'react'
import { Workflow } from 'lucide-react'
import { countLabel, type SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import { useLoggingPipeline } from '../../api/loggingPipeline'
import type { SidebarWorkspaceArgs } from '../../integrations/sidebarWorkspaces'
import { useWorkspaceNavigate } from '../../hooks/useWorkspaceNavigate'

const LOGGING_GROUP = 'logging.banzaicloud.io'
// The sidebar category api-resources assigns to the Logging operator's group.
export const LOGGING_SIDEBAR_CATEGORY = 'Logging'

/**
 * The Logging workspace entry for the Resources sidebar: a Pipeline
 * destination counting running pods nothing routes. The count reads every
 * running pod in view, so it is fetched only while the Logging category is
 * open; elsewhere the destination shows no badge.
 */
export function useLoggingSidebarWorkspace({ apiResources, namespaces, selectedKind }: SidebarWorkspaceArgs): Record<string, SidebarCategoryWorkspace> | undefined {
  const navigate = useWorkspaceNavigate()
  const available = !!apiResources?.some((r) => r.group === LOGGING_GROUP)
  const counting = available && selectedKind?.group === LOGGING_GROUP
  const { data } = useLoggingPipeline(namespaces, counting)
  const unrouted = counting ? data?.unrouted : undefined
  const nsKey = namespaces.join(', ')
  return useMemo(() => {
    if (!available) return undefined
    const label = unrouted ? countLabel(unrouted, 'running pods nothing routes') : undefined
    return {
      [LOGGING_SIDEBAR_CATEGORY]: {
        destinations: [{
          id: 'pipeline',
          label: 'Pipeline',
          icon: Workflow,
          count: unrouted ? (unrouted.lowerBound && unrouted.count === 0 ? null : unrouted.count) : undefined,
          countLowerBound: unrouted?.lowerBound,
          countTitle: label?.title,
          onSelect: () => navigate('/logging'),
        }],
        scopeNote: nsKey ? `Counts for namespace ${nsKey}` : undefined,
      },
    }
  }, [available, unrouted, navigate, nsKey])
}
