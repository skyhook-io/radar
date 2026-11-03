import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

const feature = vi.hoisted(() => ({ support: 'supported' as string }))
vi.mock('../../api/client', async (original) => ({
  ...await original<typeof import('../../api/client')>(),
  useRadarFeature: () => ({ support: feature.support }),
}))
const { useRayClusterLogsOnPodRows } = await import('./WorkloadView')

function Probe({ apiVersion }: { apiVersion?: string }) {
  return <>{String(useRayClusterLogsOnPodRows('rayclusters', 'ray.io', apiVersion))}</>
}

describe('useRayClusterLogsOnPodRows', () => {
  it.each([
    ['supported', 'ray.io/v1', 'true'],
    ['unknown', 'ray.io/v1', 'false'],
    ['unsupported', 'ray.io/v1', 'false'],
    ['supported', 'ray.io/v1alpha1', 'false'],
  ])('with Radar support %s and %s withholds the generic tab: %s', (support, apiVersion, expected) => {
    feature.support = support
    expect(renderToStaticMarkup(<Probe apiVersion={apiVersion} />)).toBe(expected)
  })
})
