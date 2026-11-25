import { useEffect } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { Unplug } from 'lucide-react'
import { useContexts } from '../../api/client'
import { useConnection } from '../../context/ConnectionContext'
import { useContextSwitchFlow } from '../useContextSwitchFlow'
import { ScreenEmptyState } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { SUBSTRATE_PATH } from './routes'

export interface ContextPin {
  pinned: string | null
  active: string
  /** The page was opened for another context: render nothing of it and read nothing. */
  mismatch: boolean
}

/**
 * The `ctx` pin every Substrate detail page carries: a link names the
 * Kubernetes context it was made in, and a missing pin is set to the active
 * one. Actors, templates and pools are named the same way in every cluster, so
 * a page opened under another context must not show that cluster's namesake.
 */
export function useContextPin(): ContextPin {
  const location = useLocation()
  const [params, setParams] = useSearchParams()
  const { connection } = useConnection()
  const active = connection.context
  const pinned = params.get('ctx')

  useEffect(() => {
    if (pinned || !active) return
    const p = new URLSearchParams(params)
    p.set('ctx', active)
    setParams(p, { replace: true, state: location.state })
  }, [pinned, active]) // eslint-disable-line react-hooks/exhaustive-deps

  return { pinned, active, mismatch: !!pinned && !!active && pinned !== active }
}

/** What a detail page shows in place of itself when its pin names another context. */
export function NotInContext({ name, what, pinnedContext, activeContext }: { name: string; what: string; pinnedContext: string; activeContext: string }) {
  const navigate = useWorkspaceNavigate()
  const { data: contexts } = useContexts()
  const { requestSwitch, confirmDialog } = useContextSwitchFlow()
  const pinned = contexts?.find((c) => c.name === pinnedContext)
  return (
    <>
      <ScreenEmptyState
        icon={Unplug}
        title={`${name} is not in ${activeContext}`}
        detail={`This link points at ${what}, read through context ${pinnedContext}. Radar does not open a same-named one from another cluster.`}
        action={
          <div className="mt-4 flex flex-wrap justify-center gap-2">
            {pinned && (
              <button type="button" onClick={() => requestSwitch(pinned)} className="btn-brand px-3 py-1.5 text-sm font-medium">
                Switch back to {pinnedContext}
              </button>
            )}
            <button type="button" onClick={() => navigate(SUBSTRATE_PATH)} className="btn-secondary px-3 py-1.5 text-sm">
              Go to the Agent Substrate Overview
            </button>
          </div>
        }
      />
      {confirmDialog}
    </>
  )
}
