import { renderToString } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { GitOpsIssue } from '../../../types/gitops-insights'
import { GitOpsIssuesBand } from './GitOpsInsightViews'

const windowIssues: GitOpsIssue[] = [
  {
    reason: 'SyncWindowBlocked', scope: 'operation', severity: 'warning',
    message: "A sync window is holding this sync. Argo CD continues it when the AppProject's sync windows allow syncing.",
    action: 'Wait for the window, or terminate the operation to drop it. A terminated operation stays Terminating until a window allows syncing; it applies nothing more, and changes already applied stay.',
  },
  {
    reason: 'SyncWindowBlocked', scope: 'operation', severity: 'warning',
    message: 'This operation was terminated while a sync window held it. Argo CD completes the termination when a window allows syncing. It applies nothing more; changes already applied stay.',
    action: "Wait for the window to open, or change the AppProject's sync windows.",
  },
]

describe('sync-window insights', () => {
  it.each(windowIssues)('shows the full message and action for $message', (issue) => {
    for (const issues of [[issue], [issue, { ...issue, reason: 'OtherIssue' }]]) {
      const html = renderToString(<GitOpsIssuesBand issues={issues} />)
      expect(html).toMatch(/<span class="[^"]*whitespace-normal break-words">/)
      expect(html).not.toMatch(/<span class="[^"]*truncate">/)
      expect(html).toContain(issue.message.replaceAll("'", '&#x27;'))
      expect(html).toContain(`<p class="pl-[22px] text-[11px] text-theme-text-tertiary whitespace-normal break-words">${issue.action!.replaceAll("'", '&#x27;')}</p>`)
    }
  })
})
