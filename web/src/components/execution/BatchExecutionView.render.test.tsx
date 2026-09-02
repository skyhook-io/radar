import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { BatchExecutionFullscreen } from './BatchExecutionView'

vi.mock('../../api/client', () => ({
  useResource: () => ({}),
  useWorkloadPods: () => ({}),
  useWorkloadRuns: () => ({ data: { runs: [] } }),
}))

describe.each(['Job', 'CronJob', 'ScaledJob'])('%s cleanup retention display', (kind) => {
  function render(ttlSecondsAfterFinished?: number) {
    const jobSpec = { ttlSecondsAfterFinished, activeDeadlineSeconds: 60 }
    const spec = kind === 'CronJob'
      ? { jobTemplate: { spec: jobSpec } }
      : kind === 'ScaledJob'
        ? { jobTargetRef: jobSpec }
        : jobSpec
    return renderToStaticMarkup(
      <BatchExecutionFullscreen kind={kind} apiKind={`${kind.toLowerCase()}s`} namespace="default" name="example" resource={{ spec }} />,
    )
  }

  it.each([0, 300])('shows a configured TTL of %i seconds exactly once alongside the definition', (ttl) => {
    const html = render(ttl)
    expect(html.match(/TTL after finish/g)).toHaveLength(1)
    expect(html).toContain(`>${ttl}s<`)
    expect(html).toContain('>Deadline<')
    expect(html).toContain('>60s<')
  })

  it('omits TTL when cleanup retention is unconfigured', () => {
    expect(render()).not.toContain('TTL after finish')
  })
})
