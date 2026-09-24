import { useState } from 'react'
import { Sparkles, X } from 'lucide-react'
import { CloudHintLink } from './CloudHintLink'
import { dismissCloudHint, isCloudHintDismissed } from './cloudHints'

// In a team installation, where local AI investigations can't run, the issue
// list says so once instead of offering an Investigate button that leads
// nowhere. The X hides it for good. Callers gate on useCloudHintsEnabled and
// on the server reporting a shared installation.
export function SharedInstallAiLine() {
  const [hidden, setHidden] = useState(() => isCloudHintDismissed('ai-shared'))
  if (hidden) return null
  return (
    <div role="note" className="flex items-center gap-2.5 rounded-lg border border-theme-border bg-theme-base px-3 py-2">
      <Sparkles className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" aria-hidden />
      <span className="min-w-0 flex-1 text-xs text-theme-text-tertiary">
        AI investigations aren't available in this shared installation. Run Radar locally with an agent CLI, or use{' '}
        <CloudHintLink entry="ai-shared" />.
      </span>
      <button
        type="button"
        aria-label="Don't show again"
        title="Don't show again"
        onClick={() => {
          dismissCloudHint('ai-shared')
          setHidden(true)
        }}
        className="shrink-0 rounded-md p-1 text-theme-text-tertiary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
      >
        <X className="h-3.5 w-3.5" />
      </button>
    </div>
  )
}
