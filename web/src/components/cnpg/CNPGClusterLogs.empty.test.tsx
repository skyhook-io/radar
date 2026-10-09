import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { CNPGClusterLogs } from './CNPGClusterLogs'
const state = vi.hoisted(() => ({ props: {} as any, navigate: vi.fn(), problems: [{ job: 'initdb', severity: 'critical', title: 'First instance cannot be scheduled' }] as any[] }))
vi.mock('@skyhook-io/k8s-ui', async (original) => ({ ...await original<typeof import('@skyhook-io/k8s-ui')>(), WorkloadLogsViewer: (props: any) => { state.props = props; return <div>{props.emptySourceState}</div> } }))
vi.mock('../../api/client', () => ({ useRadarFeature: () => ({ guard: (fn: () => unknown) => fn(), support: 'supported' }), fetchJSON: vi.fn() }))
vi.mock('../../hooks/useDesktopDownload', () => ({ useDesktopDownload: () => undefined }))
vi.mock('../../context/ThemeContext', () => ({ useTheme: () => ({ theme: 'light' }) }))
vi.mock('./useCNPGNavigate', () => ({ useCNPGNavigate: () => state.navigate }))
vi.mock('./useCNPGSidebarWorkspace', () => ({ useCNPGFleet: () => ({ fleet: { rows: [{ namespace: 'db', name: 'analytics', problems: state.problems }] } }) }))
vi.mock('./CNPGTrends', () => ({ useCNPGIntervalParams: () => null }))
it('supplies the first-instance problem and Overview link from the CNPG host', () => {
  const html = renderToStaticMarkup(<MemoryRouter initialEntries={[{ pathname: '/cnpg/clusters/db/analytics', search: '?ctx=test&tab=logs&drawer=x', state: { returnLabel: 'Clusters' } }]}><CNPGClusterLogs namespace="db" name="analytics" /></MemoryRouter>)
  expect(html).toContain('First instance cannot be scheduled')
  expect(html).toContain('See Overview’s problem')
  state.props.emptySourceState.props.children[1].props.onClick()
  expect(state.navigate).toHaveBeenCalledWith('/cnpg/clusters/db/analytics?ctx=test&tab=overview&drawer=x&problems=all', { replace: true, state: { returnLabel: 'Clusters' } })
  expect(state.props.disableSourceControlsWithoutSource).toBe(true)
})

it('does not invent a first-instance cause or use an unrelated problem', () => {
  state.problems = [{ severity: 'critical', title: 'Backup failed' }]
  const html = renderToStaticMarkup(<MemoryRouter><CNPGClusterLogs namespace="db" name="analytics" /></MemoryRouter>)
  expect(html).toContain('No instance log source yet')
  expect(html).toContain('Open Overview')
  expect(html).not.toContain('first instance has not started')
  expect(html).not.toContain('Backup failed')
})
