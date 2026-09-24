import { useState } from 'react'
import { Bell, X } from 'lucide-react'
import { categoryLabel, type Issue } from '@skyhook-io/k8s-ui'
import {
  dismissCloudHint,
  hideIssueHint,
  isCloudHintDismissed,
  isIssueHintHidden,
  issueHintKey,
  openCloudFunnel,
} from './cloudHints'

// The last section of an expanded issue: an offer to be alerted next time.
// Alerts need something watching the cluster while Radar is closed, which is
// Radar Cloud, and the prompt says so up front. Two ways out: the X hides it on
// this issue for the tab, "Don't show again" hides it on every issue for good.
// Callers gate on useCloudHintsEnabled.
export function CloudAlertPrompt({ issue, context }: { issue: Issue; context?: string }) {
  const key = issueHintKey(context, issue.id)
  const [hidden, setHidden] = useState(() => isCloudHintDismissed('alert-issue') || isIssueHintHidden(key))
  if (hidden) return null
  return (
    <section
      aria-label="Alerts"
      className="flex items-center gap-2.5 rounded-lg border border-theme-border bg-theme-base px-3 py-2"
    >
      <Bell className="h-4 w-4 shrink-0 text-accent" aria-hidden />
      <span className="min-w-0 flex-1 text-[13px] text-theme-text-secondary">
        Get alerted when this happens again with{' '}
        <span className="font-semibold text-theme-text-primary">Radar Cloud</span>.
      </span>
      <button
        type="button"
        onClick={() =>
          openCloudFunnel('alert-issue', {
            intent: 'alert',
            category: categoryLabel(issue.category),
            categoryRaw: issue.category,
            issueId: issue.id,
            kind: issue.kind,
            group: issue.group,
            name: issue.name,
            namespace: issue.namespace,
            context,
          })
        }
        className="shrink-0 whitespace-nowrap rounded-md border border-theme-border px-2.5 py-1 text-xs font-medium text-accent-text transition-colors hover:bg-theme-hover"
      >
        Set up alert
      </button>
      <button
        type="button"
        onClick={() => {
          dismissCloudHint('alert-issue')
          setHidden(true)
        }}
        className="shrink-0 whitespace-nowrap px-1 text-xs text-theme-text-tertiary transition-colors hover:text-theme-text-primary"
      >
        Don't show again
      </button>
      <button
        type="button"
        aria-label="Hide for this issue"
        title="Hide for this issue"
        onClick={() => {
          hideIssueHint(key)
          setHidden(true)
        }}
        className="shrink-0 rounded-md p-1 text-theme-text-tertiary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
      >
        <X className="h-3.5 w-3.5" />
      </button>
    </section>
  )
}
