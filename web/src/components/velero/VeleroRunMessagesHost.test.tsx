import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

vi.mock('../../api/policy', () => ({ useVeleroRunMessages: () => ({ isPending: false, mutate: vi.fn() }) }))

const { VeleroRunIssueMessages, veleroMessagesInIssue } = await import('./VeleroRunMessagesHost')

const run = (phase: string) => ({ metadata: { namespace: 'velero', name: 'nightly-1' }, status: { phase, errors: 9, warnings: 3 } })

// The messages are what is wrong with a failed run, so they sit in its issue;
// only one place shows them.
describe('where a Velero run’s messages go', () => {
  it('goes in the issue when the drawer shows it, in Status otherwise', () => {
    expect(veleroMessagesInIssue(run('PartiallyFailed'), true)).toBe(true)
    expect(veleroMessagesInIssue(run('PartiallyFailed'), false)).toBe(false)
    // A completed run with warnings raises no issue.
    expect(veleroMessagesInIssue(run('Completed'), true)).toBe(false)
  })

  it('renders under a failed run’s issue only', () => {
    const html = renderToStaticMarkup(<VeleroRunIssueMessages issue={{ reason: 'BackupPartiallyFailed' }} resource={run('PartiallyFailed')} />)
    expect(html).toContain('Show 9 errors and 3 warnings')
    expect(renderToStaticMarkup(<VeleroRunIssueMessages issue={{ reason: 'VeleroRunStalled' }} resource={run('PartiallyFailed')} />)).toBe('')
  })
})
