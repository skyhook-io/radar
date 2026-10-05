import { useEffect, useId, useMemo, useState } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { Plug, X } from 'lucide-react'
import { CNPGConnectSection, DialogPortal, Tooltip, type ResourceRef, type NavigateToResource } from '@skyhook-io/k8s-ui'
import { buildWorkloadPath, refToSelectedResource } from '../../../utils/navigation'
import { useCNPGFleet } from '../useCNPGSidebarWorkspace'
import { Notice, RefreshFailedNotice } from '../../workspace/layout'
import { useCNPGNavigate } from '../useCNPGNavigate'

// A link can ask for the dialog (the restore "Next steps") with this param;
// the button opens it and drops the param, so closing it and following a link
// from it never write the URL at once. The value names the Cluster and the
// surface, so a drawer showing the page's own Cluster never opens a second one.
export const CNPG_CONNECT_PARAM = 'connect'

export function cnpgConnectParamValue(namespace: string, name: string, surface: 'page' | 'drawer'): string {
  return surface === 'drawer' ? `${namespace}/${name}@drawer` : `${namespace}/${name}`
}

// A request is answered by one button: a full-screen drawer can carry the
// same surface as the page beneath it, and both see the request in the same
// commit. A button that answered and then unmounted (before the param was
// dropped) gives the request back, so its remount can answer it.
const mountedButtons = new Set<symbol>()
let answered: { key: string; by: symbol } | null = null

export function registerConnectButton(id: symbol): () => void {
  mountedButtons.add(id)
  return () => {
    mountedButtons.delete(id)
  }
}

/** True when this button should open the dialog for the request at locationKey. */
export function claimConnectRequest(locationKey: string, id: symbol): boolean {
  if (answered && answered.key === locationKey && answered.by !== id && mountedButtons.has(answered.by)) return false
  answered = { key: locationKey, by: id }
  return true
}

export function CNPGConnectButton({
  namespace,
  name,
  compact = false,
  onNavigate,
}: {
  namespace: string
  name: string
  compact?: boolean
  onNavigate?: NavigateToResource
}) {
  const titleId = useId()
  const location = useLocation()
  const [params, setParams] = useSearchParams()
  const [open, setOpen] = useState(false)
  const id = useMemo(() => Symbol('cnpg-connect'), [])
  useEffect(() => registerConnectButton(id), [id])
  const requested = params.get(CNPG_CONNECT_PARAM) === cnpgConnectParamValue(namespace, name, compact ? 'drawer' : 'page')
  useEffect(() => {
    if (!requested || !claimConnectRequest(location.key, id)) return
    setOpen(true)
    // Keeps the page's return label, which lives in the history state.
    setParams(
      (prev) => {
        const p = new URLSearchParams(prev)
        p.delete(CNPG_CONNECT_PARAM)
        return p
      },
      { replace: true, state: location.state },
    )
  }, [requested, id, setParams, location.key, location.state])
  const { query, fleet } = useCNPGFleet([namespace], open)
  const row = fleet?.rows.find((r) => r.namespace === namespace && r.name === name)
  const go = onNavigate
    ? (ref: ResourceRef) => {
        setOpen(false)
        onNavigate(refToSelectedResource(ref))
      }
    : undefined
  const navigate = useCNPGNavigate()
  const openReachability = (svc: { namespace: string; name: string }) => {
    setOpen(false)
    navigate(buildWorkloadPath({ kind: 'services', group: '', namespace: svc.namespace, name: svc.name, tab: 'reachability' }))
  }

  return (
    <>
      <Tooltip content="Hosts, database, owner and the credentials Secret applications use" position="bottom">
        <button
          type="button"
          onClick={() => setOpen(true)}
          aria-label={compact ? 'Connect' : undefined}
          className="btn-secondary inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap px-2.5 py-1.5 text-xs font-medium"
        >
          <Plug className="h-3.5 w-3.5" />
          {!compact && 'Connect'}
        </button>
      </Tooltip>
      <DialogPortal open={open} onClose={() => setOpen(false)} className="w-full max-w-3xl" ariaLabelledBy={titleId}>
        <div className="flex items-start gap-3 border-b border-theme-border p-4">
          <div className="min-w-0 flex-1">
            <h3 id={titleId} className="text-lg font-semibold text-theme-text-primary">
              Connect to {name}
            </h3>
            <p className="mt-0.5 text-xs text-theme-text-tertiary">From the Cluster spec · hosts resolve inside the Kubernetes cluster</p>
          </div>
          <button type="button" onClick={() => setOpen(false)} aria-label="Close" className="rounded p-1 text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary">
            <X className="h-5 w-5" />
          </button>
        </div>
        <div className="max-h-[70vh] overflow-y-auto p-4">
          <RefreshFailedNotice queries={[query]} />
          {row?.cluster ? (
            <CNPGConnectSection cluster={row.cluster} poolers={row.poolerObjects} poolersKnown={row.poolersKnown} onNavigate={go} onOpenReachability={openReachability} showHeading={false} />
          ) : query.error && !query.data ? (
            <Notice>
              Cluster could not be read: {query.error instanceof Error ? query.error.message : 'unknown error'}.{' '}
              <button type="button" onClick={() => void query.refetch()} disabled={query.isFetching} className="text-accent-text hover:underline">Retry</button>
            </Notice>
          ) : (
            <div className="text-sm text-theme-text-tertiary">{fleet ? 'Radar cannot read this Cluster with your access.' : 'Reading the Cluster…'}</div>
          )}
        </div>
      </DialogPortal>
    </>
  )
}
