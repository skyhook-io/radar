import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { useAPIResources } from '../../api/apiResources'
import { useVulnerabilities, type VulnerableImage } from '../../api/vulnerabilities'
import { WorkloadVulnerabilitiesSection } from './WorkloadVulnerabilitiesSection'

vi.mock('../../api/apiResources', () => ({ useAPIResources: vi.fn() }))
vi.mock('../../api/client', () => ({ useRadarFeature: () => ({ support: 'supported' }) }))
vi.mock('../../api/vulnerabilities', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../api/vulnerabilities')>(),
  useVulnerabilities: vi.fn(),
}))

const workload = { kind: 'Deployment', group: 'apps', namespace: 'team', name: 'api', container: 'server' }
const zero = { critical: 0, high: 0, medium: 0, low: 0, unknown: 0 }
const image: VulnerableImage = {
  id: 'api', image: 'example.com/team/api:1', scannedAt: '2026-11-20T12:00:00Z',
  counts: { ...zero, critical: 1 }, fixable: { ...zero, critical: 1 }, matching: 1,
  workloads: [workload], workloadsTotal: 1, findings: [], findingsTotal: 1,
  report: { kind: 'VulnerabilityReport', namespace: 'team', name: 'deployment-api' },
}

function renderDrawer(available: boolean, reports: { images: VulnerableImage[]; clean?: VulnerableImage[]; unscanned?: { image: string }[] }) {
  vi.mocked(useAPIResources).mockReturnValue({
    data: available ? [{ group: 'aquasecurity.github.io', name: 'vulnerabilityreports' }] : [],
  } as ReturnType<typeof useAPIResources>)
  vi.mocked(useVulnerabilities).mockImplementation(({ view }) => ({
    data: view === 'image' ? reports : { fixes: [] },
  } as ReturnType<typeof useVulnerabilities>))
  return renderToStaticMarkup(
    <MemoryRouter>
      <WorkloadVulnerabilitiesSection kind={workload.kind} namespace={workload.namespace} name={workload.name} />
    </MemoryRouter>,
  )
}

it('omits the section when Trivy is unavailable or has no findings for the workload', () => {
  expect(renderDrawer(false, { images: [] })).toBe('')
  expect(renderDrawer(true, { images: [], clean: [{ ...image, counts: zero, findingsTotal: 0 }] })).toBe('')
})

it('shows reported findings without an advisory scan action', () => {
  const html = renderDrawer(true, { images: [image] })
  expect(html).toContain('Vulnerabilities · 1 critical')
  expect(html).toContain('1 with a fix')
  expect(html).not.toContain('Java4Shell')
})

it('discloses missing scans alongside reported findings', () => {
  const html = renderDrawer(true, { images: [image], unscanned: [{ image: 'example.com/sidecar' }] })
  expect(html).toContain('View unscanned images in this workload')
  expect(html).not.toContain('Java4Shell')
})
