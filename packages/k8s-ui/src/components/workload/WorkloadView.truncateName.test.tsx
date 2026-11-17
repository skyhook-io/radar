// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { WorkloadView } from './WorkloadView'

vi.mock('../shared/ResourceRendererDispatch', async (original) => ({
  ...await original<typeof import('../shared/ResourceRendererDispatch')>(),
  ResourceRendererDispatch: () => <div />,
}))

const backup = { apiVersion: 'velero.io/v1', kind: 'Backup', metadata: { name: 'live-partial', namespace: 'velero' }, status: { phase: 'PartiallyFailed' } }

// A title kept on one line still needs room to be read: on a narrow page the
// badges beside it wrap among themselves before the name is cut to nothing.
describe('a title kept on one line', () => {
  it('keeps the name a readable width and lets the badges wrap', () => {
    const html = renderToStaticMarkup(
      <WorkloadView kind="backups" group="velero.io" namespace="velero" name="live-partial" resource={backup} onBack={() => {}} activeTab="overview" expanded inlineBadges truncateName />,
    )
    expect(html).toContain('min-w-[min(12rem,100%)]')
    expect(html).toMatch(/class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1"[^>]*>.*Namespace: /)
    expect(html).not.toContain('flex shrink-0 items-center gap-x-3')
  })
})
