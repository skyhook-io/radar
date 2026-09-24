import { useEffect, useState } from 'react'
import { Globe, X } from 'lucide-react'
import { CloudHintLink } from './CloudHintLink'
import { dismissCloudHint, isCloudHintDismissed, useCloudHintsEnabled } from './cloudHints'
import { useCapabilities } from '../../api/client'
import { CONTEXT_SWITCHED_EVENT, readTriggeredPair } from './contextSwitchLog'
import { Tooltip } from '../ui/Tooltip'

// Fixed so the host can move its drawers down by exactly this much while the
// row is showing.
export const CONTEXT_SWITCH_ROW_HEIGHT = 32

// Pages that list something across the whole cluster and have a fleet-wide
// counterpart in Radar Cloud, with the noun the row uses for what they list.
// Elsewhere (a single resource, topology, a timeline) comparing clusters side
// by side is not what Cloud offers, so the row stays away.
const AGGREGATE_PAGES: Record<string, string> = {
  issues: 'issues',
  checks: 'check results',
  helm: 'Helm releases',
  gitops: 'GitOps apps',
  applications: 'applications',
}

// Shows once someone has switched contexts enough to look like they are
// comparing clusters, on the aggregate pages only, and stays for the rest of
// the tab until the X hides it for good.
export function useContextSwitchCloudRow(view: string) {
  const enabled = useCloudHintsEnabled()
  const capabilities = useCapabilities()
  const [names, setNames] = useState<[string, string] | null>(null)
  const [dismissed, setDismissed] = useState(() => isCloudHintDismissed('context-switch'))
  useEffect(() => {
    if (!enabled || dismissed) return
    const sync = () => setNames(readTriggeredPair())
    sync()
    window.addEventListener(CONTEXT_SWITCHED_EVENT, sync)
    return () => window.removeEventListener(CONTEXT_SWITCHED_EVENT, sync)
  }, [enabled, dismissed])
  // Kept while capabilities reload: the switch that raised the row also
  // clears every query, and re-gating on the loading state would blink the
  // row (and jump the drawers below it). A reload that settles with Radar
  // Cloud off does hide it.
  const settledOff = capabilities.isSuccess && !enabled
  const what = Object.hasOwn(AGGREGATE_PAGES, view) ? AGGREGATE_PAGES[view] : undefined
  return {
    names: dismissed || !what || settledOff ? null : names,
    what: what ?? '',
    dismiss: () => {
      dismissCloudHint('context-switch')
      setDismissed(true)
    },
  }
}

export function ContextSwitchCloudRow({ names, what, onDismiss }: { names: [string, string]; what: string; onDismiss: () => void }) {
  return (
    <div
      role="status"
      className="flex shrink-0 items-center gap-2.5 border-b border-theme-border bg-theme-base pl-4 pr-3"
      style={{ height: CONTEXT_SWITCH_ROW_HEIGHT }}
    >
      <Globe className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" aria-hidden />
      <span className="min-w-0 flex-1 truncate text-xs text-theme-text-tertiary">
        See {what} from {names[0]} and {names[1]} side by side in <CloudHintLink entry="context-switch" />.
      </span>
      <Tooltip content="Don't show again" delay={100} wrapperClassName="shrink-0">
        <button
          type="button"
          aria-label="Don't show again"
          onClick={onDismiss}
          className="shrink-0 rounded-md p-1 text-theme-text-tertiary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </Tooltip>
    </div>
  )
}
