import { useMemo } from 'react'
import { Workflow } from 'lucide-react'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import type { APIResource } from '../../types'
import { useRadarFeature } from '../../api/client'
import { useKargoWorkspace, type KargoWorkspaceResponse } from '../../api/kargo'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import type { CommandItem } from '../ui/command-items'
import { KARGO_PATH, KARGO_SIDEBAR_CATEGORY, KARGO_SIDEBAR_LABEL, KARGO_TITLE, kargoDiscovered } from './routes'

/**
 * The Kargo problems across the projects read, each Issues finding once. A
 * lower bound when some namespaces' Stages could not be read.
 */
export function kargoProblemCount(data: KargoWorkspaceResponse | undefined): { count?: number; lowerBound?: boolean; title?: string } {
  if (!data?.installed) return {}
  const count = data.projects.reduce((n, p) => n + p.problems, 0)
  const stages = data.coverage.stages?.state
  const lowerBound = stages !== undefined && stages !== 'full'
  if (count === 0 && !lowerBound) return { count: 0 }
  const noun = count === 1 ? 'problem' : 'problems'
  return {
    count,
    lowerBound,
    title: lowerBound ? `At least ${count} ${noun}: some projects could not be read` : `${count} ${noun}`,
  }
}

/** The pipelines page in the Cmd+K palette, found by "kargo" or "pipeline". */
export function kargoCommandItem(open: () => void): CommandItem {
  return { id: 'view-kargo', label: `Go to ${KARGO_TITLE}`, category: 'Views', icon: Workflow, action: open, searchTerms: ['kargo', 'pipelines', 'promotions'] }
}

/**
 * Kargo in the sidebar's Integrations: one row with the Kargo problems across
 * every project the caller can read, opening to the pipelines screen.
 * Undefined when Kargo is not discovered, or the connected Radar predates the
 * screen. The namespace filter does not apply.
 */
export function useKargoSidebarWorkspace({
  apiResources,
  active,
}: {
  apiResources: APIResource[] | undefined
  /** The pipelines screen is on screen. */
  active?: boolean
}): Record<string, SidebarCategoryWorkspace> | undefined {
  const navigate = useWorkspaceNavigate()
  const { support } = useRadarFeature('kargoWorkspace')
  const available = kargoDiscovered(apiResources) && support !== 'unsupported'
  const { data } = useKargoWorkspace({ enabled: available })
  const counts = useMemo(() => kargoProblemCount(data), [data])
  return useMemo(() => {
    if (!available) return undefined
    return {
      [KARGO_SIDEBAR_CATEGORY]: {
        label: KARGO_SIDEBAR_LABEL,
        icon: Workflow,
        count: counts.count,
        countLowerBound: counts.lowerBound,
        countTitle: counts.title,
        destinations: [
          { id: 'pipelines', label: 'Pipelines', active: !!active, onSelect: () => navigate(KARGO_PATH) },
        ],
      },
    }
  }, [available, active, navigate, counts])
}
