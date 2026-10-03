import { useEffect, useId, useState } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { Plug, X } from 'lucide-react'
import { CNPGConnectSection, DialogPortal, Tooltip, type CNPGRef, type NavigateToResource } from '@skyhook-io/k8s-ui'
import { refToSelectedResource } from '../../../utils/navigation'
import { useCNPGFleet } from '../useCNPGSidebarWorkspace'

// A link can ask for the dialog (the restore "Next steps") with this param;
// the button opens it and drops the param, so closing it and following a link
// from it never write the URL at once. The value names the Cluster and the
// surface, so a drawer showing the page's own Cluster never opens a second one.
export const CNPG_CONNECT_PARAM = 'connect'

export function cnpgConnectParamValue(namespace: string, name: string, surface: 'page' | 'drawer'): string {
  return surface === 'drawer' ? `${namespace}/${name}@drawer` : `${namespace}/${name}`
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
  const requested = params.get(CNPG_CONNECT_PARAM) === cnpgConnectParamValue(namespace, name, compact ? 'drawer' : 'page')
  useEffect(() => {
    if (!requested) return
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
  }, [requested, setParams, location.state])
  const { fleet } = useCNPGFleet([namespace], open)
  const row = fleet?.rows.find((r) => r.namespace === namespace && r.name === name)
  const go = onNavigate
    ? (ref: CNPGRef) => {
        setOpen(false)
        onNavigate(refToSelectedResource(ref))
      }
    : undefined

  return (
    <>
      <Tooltip content="Hosts, database, owner and the credentials Secret applications use" position="bottom">
        <button
          type="button"
          onClick={() => setOpen(true)}
          aria-label={compact ? 'Connect' : undefined}
          className="inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-lg border border-theme-border bg-theme-surface px-2.5 py-1.5 text-xs font-medium text-theme-text-primary hover:bg-theme-hover"
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
          {row?.cluster ? (
            <CNPGConnectSection cluster={row.cluster} poolers={row.poolerObjects} poolersKnown={row.poolersKnown} onNavigate={go} showHeading={false} />
          ) : (
            <div className="text-sm text-theme-text-tertiary">{fleet ? 'This Cluster is not in the CloudNativePG workspace for your identity.' : 'Reading the Cluster…'}</div>
          )}
        </div>
      </DialogPortal>
    </>
  )
}
