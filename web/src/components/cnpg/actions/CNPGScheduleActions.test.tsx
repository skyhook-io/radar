// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { CNPGScheduleActions } from './CNPGScheduleActions'
const navigate = vi.hoisted(() => vi.fn())
const schedule = vi.hoisted(() => ({ cluster: 'payments', reason: 'Configure a backup destination on payments first', backupBlockedReason: 'Configure a backup destination on payments first', clusterState: 'ok', reasonCode: 'backup_destination' }))
vi.mock('../useCNPGNavigate', () => ({ useCNPGNavigate: () => navigate }))
vi.mock('../../../api/cnpg', () => ({ useCNPGScheduleCapabilities: () => ({ data: { actions: { run: { allowed: false, reason: schedule.reason, reasonCode: schedule.reasonCode }, suspend: { allowed: true }, setSchedule: { allowed: true } }, context: 'viewer', facts: { cluster: schedule.cluster, clusterState: schedule.clusterState, backupBlockedReason: schedule.backupBlockedReason, suspended: false } } }) }))
it('shows the server destination blocker beneath the disabled Run now control', () => {
  const html = renderToStaticMarkup(<CNPGScheduleActions namespace="prod" name="payments-nightly" />)
  expect(html).toMatch(/disabled=""[^>]*>Run now/)
  expect(html.replace(/<[^>]*>/g, '')).toContain('Configure a backup destination on payments first')
  expect(html).toContain('Suspend')
})

it('keeps actions in one group and opens the target Cluster Backups tab', () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const host = document.createElement('div'); const root = createRoot(host)
  act(() => root.render(<CNPGScheduleActions namespace="prod" name="payments-nightly" />))
  const run = [...host.querySelectorAll('button')].find((b) => b.textContent === 'Run now')!
  expect(run.parentElement!.parentElement!.textContent).toBe('Run nowSuspendEdit schedule')
  const cluster = [...host.querySelectorAll('button')].find((b) => b.textContent === 'Cluster payments Backups →')!
  expect(run.parentElement!.parentElement!.contains(cluster)).toBe(false)
  act(() => cluster.click())
  expect(navigate).toHaveBeenCalledWith('/cnpg/clusters/prod/payments?ctx=viewer&tab=backups')
  act(() => root.unmount())
})

it.each(['backup', 'a', 'on'])('links the Cluster named %s rather than an earlier word in the reason', (cluster) => {
  schedule.cluster = cluster; schedule.reason = `Configure a backup destination on ${cluster} first`; schedule.backupBlockedReason = schedule.reason
  const html = renderToStaticMarkup(<CNPGScheduleActions namespace="prod" name="nightly" />)
  expect(html).toMatch(new RegExp(`<button[^>]*>Cluster ${cluster} Backups →</button>`))
  schedule.cluster = 'payments'; schedule.reason = 'Configure a backup destination on payments first'; schedule.backupBlockedReason = schedule.reason
})
it('adds the Cluster Backups destination when the blocker does not name the Cluster', () => {
  schedule.reason = 'Set a volumeSnapshot backup configuration on the Cluster'; schedule.backupBlockedReason = schedule.reason
  const html = renderToStaticMarkup(<CNPGScheduleActions namespace="prod" name="nightly" />)
  expect(html).toContain('Set a volumeSnapshot backup configuration on the Cluster')
  expect(html).toContain('Cluster payments Backups →')
  schedule.reason = 'Configure a backup destination on payments first'; schedule.backupBlockedReason = schedule.reason
})

it.each(['Your account may not do this', 'The admission webhook rejects writes', 'The schedule is being deleted', 'The cluster of the schedule does not exist in this namespace'])('does not send an unrelated blocker to Backups: %s', (reason) => {
  schedule.reason = reason
  schedule.reasonCode = 'other'
  if (reason.includes('does not exist')) schedule.clusterState = 'missing'
  const html = renderToStaticMarkup(<CNPGScheduleActions namespace="prod" name="nightly" />)
  expect(html).toContain(reason)
  expect(html).not.toContain('Cluster payments Backups')
  expect(html).not.toMatch(/<button[^>]*>payments</)
  schedule.reasonCode = 'backup_destination'; schedule.clusterState = 'ok'; schedule.reason = 'Configure a backup destination on payments first'; schedule.backupBlockedReason = schedule.reason
})
