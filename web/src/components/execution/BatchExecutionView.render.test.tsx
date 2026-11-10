import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { BatchExecutionFullscreen } from './BatchExecutionView'

const runQueryState = vi.hoisted(() => {
  const state: { isLoading: boolean; error: Error | undefined } = { isLoading: false, error: undefined }
  return state
})

vi.mock('../../api/client', () => ({
  useResource: () => ({}),
  useWorkloadPods: () => ({}),
  useJobSetResources: () => ({ data: undefined, isLoading: false }),
  useWorkloadRuns: () => ({ ...runQueryState, data: { collection: 'runs', runs: [], total: 0, truncated: false } }),
}))

it('keeps CronJob configuration distinct from scheduling and completion', () => {
  const html = renderToStaticMarkup(<BatchExecutionFullscreen
    kind="CronJob" apiKind="cronjobs" namespace="default" name="nightly"
    resource={{ spec: { schedule: '0 * * * *', timeZone: 'UTC', startingDeadlineSeconds: 0 }, status: {} }}
  />)
  expect(html).toContain('Enabled')
  expect(html).toContain('>UTC<')
  expect(html).toContain('>0s<')
})

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

it.each(['loading', 'error'])('keeps scheduling evidence visible while run history is %s', (state) => {
  runQueryState.isLoading = state === 'loading'
  runQueryState.error = state === 'error' ? new Error('history unavailable') : undefined
  try {
    const html = renderToStaticMarkup(<BatchExecutionFullscreen kind="CronJob" apiKind="cronjobs" namespace="default" name="nightly" resource={{ spec: {} }} leadContent={<p>Scheduling error reported</p>} />)
    expect(html).toContain('Scheduling error reported')
    if (state === 'error') expect(html).toContain('Run history unavailable')
  } finally {
    runQueryState.isLoading = false
    runQueryState.error = undefined
  }
})
