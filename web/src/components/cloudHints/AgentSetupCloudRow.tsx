import { ArrowUpRight } from 'lucide-react'
import { openCloudFunnel } from './cloudHints'

// One more option under the local agent list, drawn like the agent cards so it
// reads as a choice, not an ad: its name where theirs is, "Learn more" where
// their Docs link is, and what it does where their install command is. It
// always follows the local options. Callers gate on useCloudHintsEnabled.
export function AgentSetupCloudRow() {
  return (
    <div className="mt-2.5 rounded-lg border border-theme-border bg-theme-base p-3">
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="text-sm font-medium text-theme-text-primary">Radar Cloud</span>
        <button
          type="button"
          onClick={() => openCloudFunnel('ai-setup')}
          className="inline-flex items-center gap-1 text-xs text-theme-text-tertiary hover:text-theme-text-primary"
        >
          Learn more
          <ArrowUpRight className="h-3 w-3" aria-hidden />
        </button>
      </div>
      <p className="rounded-md bg-theme-elevated px-2 py-1.5 text-xs leading-relaxed text-theme-text-secondary">
        A hosted agent in your cluster, nothing to install here. It also investigates on its own when an alert fires.
      </p>
    </div>
  )
}
