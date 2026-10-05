import { useEffect } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { Unplug } from 'lucide-react'
import { useConnection } from '../../context/ConnectionContext'
import { useContexts } from '../../api/client'
import { useContextSwitchFlow } from '../useContextSwitchFlow'
import { useWorkspaceNavigate } from './useWorkspaceNavigate'
import { ScreenEmptyState } from './layout'
export function usePinnedDetailContext() {
  const { connection } = useConnection(),
    location = useLocation()
  const [params, setParams] = useSearchParams()
  const pinnedContext = params.get('ctx'),
    activeContext = connection.context
  useEffect(() => {
    if (pinnedContext || !activeContext) return
    const next = new URLSearchParams(params)
    next.set('ctx', activeContext)
    setParams(next, { replace: true, state: location.state })
  }, [pinnedContext, activeContext]) // eslint-disable-line react-hooks/exhaustive-deps
  return {
    activeContext,
    pinnedContext,
    mismatched:
      !!pinnedContext && !!activeContext && pinnedContext !== activeContext,
  }
}
export function WorkspaceContextMismatch({
  name,
  pinnedContext,
  activeContext,
  homePath,
  homeLabel,
}: {
  name: string
  pinnedContext: string
  activeContext: string
  homePath: string
  homeLabel: string
}) {
  const navigate = useWorkspaceNavigate(),
    { data: contexts } = useContexts(),
    { requestSwitch, confirmDialog } = useContextSwitchFlow()
  const pinned = contexts?.find((c) => c.name === pinnedContext)
  return (
    <>
      <ScreenEmptyState
        icon={Unplug}
        title={`${name} is not in ${activeContext}`}
        detail={`This link belongs to context ${pinnedContext}. Radar does not open a same-named object in another context.`}
        action={
          <div className="mt-4 flex flex-wrap justify-center gap-2">
            {pinned && (
              <button
                className="btn-brand px-3 py-1.5 text-sm"
                onClick={() => requestSwitch(pinned)}
              >
                Switch back to {pinnedContext}
              </button>
            )}
            <button
              className="btn-secondary px-3 py-1.5 text-sm"
              onClick={() => navigate(homePath)}
            >
              Go to {homeLabel}
            </button>
          </div>
        }
      />
      {confirmDialog}
    </>
  )
}
