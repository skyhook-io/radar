// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { evaluateGitOpsWriteGuard, type GitOpsWriteGuardInput, type VeleroResumeChoices } from '@skyhook-io/k8s-ui'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const target = { kind: 'Schedule', group: 'velero.io', namespace: 'velero', name: 'nightly' }
const argoSelfHeal: Partial<GitOpsWriteGuardInput> = {
  owner: { tool: 'argocd', kind: 'applications', namespace: 'argocd', name: 'backups' },
  evidence: {
    uid: 'u',
    resourceVersion: '42',
    owner: null,
    policy: { tool: 'argocd', auto: true, selfHeal: true, prune: false, suspended: null },
    paths: [
      { path: 'spec.paused', lastApplied: 'present', ownedBy: [], ownedByGitOps: false },
      { path: 'spec.skipImmediately', lastApplied: 'absent', ownedBy: [], ownedByGitOps: false },
    ],
  },
}

const state = vi.hoisted(() => ({
  support: 'supported',
  mutate: vi.fn(),
  error: null as Error | null,
  resume: undefined as unknown,
  serverStopped: false,
  backupsDisabled: false,
  guardInput: {} as Record<string, unknown>,
  toast: vi.fn(),
  refetch: vi.fn(),
  updatedAt: 1,
}))
vi.mock('../../api/client', () => ({ useRadarFeature: () => ({ support: state.support }) }))
vi.mock('../../api/velero', () => ({
  useVeleroSchedulePause: () => ({ mutate: state.mutate, isPending: false, error: state.error }),
  useVeleroScheduleActivity: () => ({ data: { resume: state.resume, serverStopped: state.serverStopped, backupsDisabled: state.backupsDisabled }, refetch: state.refetch, isPending: false, dataUpdatedAt: state.updatedAt }),
}))
vi.mock('../../hooks/useGitOpsWriteGuard', () => ({
  useGitOpsWriteGuard: ({ writes }: { writes: GitOpsWriteGuardInput['writes'] }) => ({
    guard: evaluateGitOpsWriteGuard({ target, owner: null, writes, ...state.guardInput }),
  }),
}))
vi.mock('../workspace/useWorkspaceNavigate', () => ({ useWorkspaceNavigate: () => vi.fn() }))
vi.mock('../ui/Toast', () => ({ showApiSuccess: state.toast }))

const { VeleroSchedulePause } = await import('./VeleroSchedulePause')

const active = { metadata: { namespace: 'velero', name: 'nightly', resourceVersion: '42' }, spec: { schedule: '0 1 * * *' } }
const paused = { ...active, spec: { ...active.spec, paused: true } }

const roots: ReturnType<typeof createRoot>[] = []
function render(node: React.ReactNode) {
  const host = document.createElement('div')
  document.body.append(host)
  const root = createRoot(host)
  roots.push(root)
  act(() => root.render(node))
  return root
}
afterEach(() => {
  act(() => roots.splice(0).forEach((r) => r.unmount()))
  document.body.innerHTML = ''
  state.support = 'supported'
  state.error = null
  state.resume = undefined
  state.serverStopped = false
  state.backupsDisabled = false
  state.guardInput = {}
  state.toast.mockReset()
  state.mutate.mockReset()
  state.refetch.mockReset()
  state.updatedAt = 1
  vi.useRealTimers()
})

const buttons = (label: string) => [...document.querySelectorAll('button')].filter((b) => b.textContent?.trim() === label)
const text = () => document.body.textContent ?? ''
const checkbox = (label: string) =>
  [...document.querySelectorAll('label')].find((l) => l.textContent?.includes(label))?.querySelector('input') as HTMLInputElement | undefined

const due: VeleroResumeChoices = {
  default: { outcome: 'runsNow', due: true, summary: 'A run is due, so Velero submits it as soon as the schedule resumes.' },
  skipping: { outcome: 'skips', due: true, summary: 'Velero skips the run due now, if any, and submits the next one at its scheduled time.' },
}

describe('Pause and resume', () => {
  it('pauses, saying what stops and that a started run continues, bound to the reviewed version', () => {
    render(<VeleroSchedulePause schedule={active} />)
    act(() => buttons('Pause')[0].click())
    expect(text()).toContain('Velero stops submitting this schedule’s runs until it is resumed.')
    expect(text()).toContain('A run already started continues to the end.')
    expect(text()).toContain('Back up now still creates a backup from this schedule while it is paused.')
    act(() => buttons('Pause schedule')[0].click())
    expect(state.mutate).toHaveBeenCalledWith(
      { namespace: 'velero', name: 'nightly', paused: true, resourceVersion: '42', skipImmediately: null },
      expect.anything(),
    )
  })

  it('does not promise Back up now while Velero runs restore-only', () => {
    state.backupsDisabled = true
    render(<VeleroSchedulePause schedule={active} />)
    act(() => buttons('Pause')[0].click())
    expect(text()).toContain('Back up now stays unavailable while Velero runs restore-only.')
    expect(text()).not.toContain('Back up now still creates a backup')
  })

  it('resumes with the server deciding the skip unless the user skips the due run', () => {
    state.resume = due
    render(<VeleroSchedulePause schedule={paused} />)
    act(() => buttons('Resume')[0].click())
    expect(text()).toContain(due.default.summary)
    const skip = checkbox('Skip the run that’s due now')!
    expect(skip.checked).toBe(false)
    act(() => skip.click())
    expect(text()).toContain(due.skipping!.summary)
    expect(text()).not.toContain(due.default.summary)
    act(() => buttons('Resume schedule')[0].click())
    expect(state.mutate).toHaveBeenCalledWith(expect.objectContaining({ paused: false, skipImmediately: true }), expect.anything())
  })

  it('reads the outlook again when the next run comes, and waits for it before confirming', () => {
    vi.useFakeTimers()
    const nextRun = new Date(Date.now() + 60_000).toISOString()
    state.resume = { default: { outcome: 'waits', due: false, summary: 'No run is due, so Velero submits the next one at its scheduled time.', nextRun, nextRunCertain: true } }
    const root = render(<VeleroSchedulePause schedule={paused} />)
    act(() => buttons('Resume')[0].click())
    const confirm = () => buttons('Resume schedule')[0] as HTMLButtonElement
    act(() => void vi.advanceTimersByTime(30_000))
    expect(state.refetch).not.toHaveBeenCalled()
    expect(confirm().disabled).toBe(false)
    act(() => void vi.advanceTimersByTime(30_000))
    expect(state.refetch).toHaveBeenCalledTimes(1)
    expect(confirm().disabled).toBe(true)
    expect(text()).toContain('Reading what resuming does')
    state.resume = due
    state.updatedAt = 2
    act(() => root.render(<VeleroSchedulePause schedule={paused} />))
    expect(confirm().disabled).toBe(false)
    expect(checkbox('Skip the run that’s due now')).toBeDefined()
    expect(text()).toContain(due.default.summary)
  })

  it('offers no skip when nothing is due', () => {
    state.resume = { default: { outcome: 'waits', due: false, summary: 'No run is due, so Velero submits the next one at its scheduled time.' } }
    render(<VeleroSchedulePause schedule={paused} />)
    act(() => buttons('Resume')[0].click())
    expect(text()).toContain('No run is due')
    expect(checkbox('Skip the run')).toBeUndefined()
    act(() => buttons('Resume schedule')[0].click())
    expect(state.mutate).toHaveBeenCalledWith(expect.objectContaining({ paused: false, skipImmediately: null }), expect.anything())
  })

  it('offers no skip on a cron with no future time, where skipping changes nothing', () => {
    const summary = 'This schedule’s cron expression has no future time, so once resumed Velero submits runs immediately and repeatedly until the cron is fixed. Skipping does not prevent it.'
    state.resume = { default: { outcome: 'runsRepeatedly', reason: 'noFutureTime', due: false, summary } }
    render(<VeleroSchedulePause schedule={paused} />)
    act(() => buttons('Resume')[0].click())
    expect(text()).toContain(summary)
    expect(checkbox('Skip the run')).toBeUndefined()
    act(() => buttons('Resume schedule')[0].click())
    expect(state.mutate).toHaveBeenCalledWith(expect.objectContaining({ paused: false, skipImmediately: null }), expect.anything())
  })

  it('refuses to confirm once the schedule changed while open', () => {
    const root = render(<VeleroSchedulePause schedule={active} />)
    act(() => buttons('Pause')[0].click())
    act(() => root.render(<VeleroSchedulePause schedule={{ ...active, metadata: { ...active.metadata, resourceVersion: '43' } }} />))
    expect(text()).toContain('This schedule changed while this was open')
    expect(buttons('Pause schedule')[0].disabled).toBe(true)
  })

  it('is disabled with a reason until the schedule has a version', () => {
    render(<VeleroSchedulePause schedule={{ ...active, metadata: { namespace: 'velero', name: 'nightly' } }} />)
    expect(buttons('Pause')[0].disabled).toBe(true)
  })

  it('warns that Argo CD reverts it, and waits for the acknowledgement', () => {
    state.guardInput = argoSelfHeal
    render(<VeleroSchedulePause schedule={active} />)
    act(() => buttons('Pause')[0].click())
    expect(text()).toContain('will revert this change')
    expect(buttons('Pause schedule')[0].disabled).toBe(true)
    act(() => checkbox('I understand Argo CD will revert this.')!.click())
    expect(buttons('Pause schedule')[0].disabled).toBe(false)
  })

  it('shows the server’s refusal in the dialog', () => {
    state.error = new Error('the schedule changed since you opened it; review it again')
    render(<VeleroSchedulePause schedule={active} />)
    act(() => buttons('Pause')[0].click())
    expect(text()).toContain('the schedule changed since you opened it')
  })

  it('says when it was done', () => {
    state.mutate.mockImplementation((_v: unknown, opts: { onSuccess: (r: unknown) => void }) => opts.onSuccess({ paused: true, changed: true }))
    render(<VeleroSchedulePause schedule={active} />)
    act(() => buttons('Pause')[0].click())
    act(() => buttons('Pause schedule')[0].click())
    expect(state.toast).toHaveBeenCalledWith('Schedule nightly paused')
  })

  it('is absent on a Radar without the action', () => {
    state.support = 'unsupported'
    render(<VeleroSchedulePause schedule={active} />)
    expect(buttons('Pause')).toHaveLength(0)
  })
})
