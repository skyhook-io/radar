// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { schedule as reading } from './velero.fixtures'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const state = vi.hoisted(() => ({ support: 'supported', mutate: vi.fn(), reading: undefined as unknown, serverStopped: false, backupsDisabled: false, toast: vi.fn() }))
vi.mock('../../api/client', () => ({ useRadarFeature: () => ({ support: state.support }) }))
vi.mock('../../api/velero', () => ({
  useVeleroBackupNow: () => ({ mutate: state.mutate, reset: vi.fn(), isPending: false }),
  useVeleroScheduleActivity: () => ({ data: state.reading ? { reading: state.reading, serverStopped: state.serverStopped } : undefined }),
  useVeleroWorkspace: () => ({ data: { installations: [{ namespace: 'velero', server: { source: 'deployment', backupsDisabled: state.backupsDisabled } }] } }),
}))
vi.mock('../ui/Toast', () => ({ showApiSuccess: state.toast }))

const { VeleroBackupNow } = await import('./VeleroBackupNow')

const nightly = {
  metadata: { namespace: 'velero', name: 'nightly', resourceVersion: '42' },
  spec: { schedule: '0 1 * * *', template: { includedNamespaces: ['payments'], storageLocation: 'primary', ttl: '168h0m0s' } },
}

// Every root is unmounted after its test: a mounted dialog keeps scheduling
// its close animation, which would otherwise run after the environment is gone.
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
  state.reading = undefined
  state.serverStopped = false
  state.backupsDisabled = false
  state.toast.mockReset()
  state.mutate.mockReset()
})

const buttonNamed = (label: string) => [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label)

describe('Back up now', () => {
  it('says a wildcard exclusion narrows what is backed up', () => {
    for (const field of ['excludedNamespaceScopedResources', 'excludedClusterScopedResources']) {
      render(<VeleroBackupNow schedule={{ ...nightly, spec: { ...nightly.spec, template: { ...nightly.spec.template, [field]: ['*'] } } }} />)
      act(() => buttonNamed('Back up now')!.click())
      expect(document.body.textContent).toContain('narrowed by resource or label filters')
      act(() => roots.splice(0).forEach((r) => r.unmount()))
      document.body.innerHTML = ''
    }
  })

  it('reads a zero retention as the server default, as Velero does', () => {
    render(<VeleroBackupNow schedule={{ ...nightly, spec: { ...nightly.spec, template: { ...nightly.spec.template, ttl: '0s' } } }} />)
    act(() => buttonNamed('Back up now')!.click())
    expect(document.body.textContent).toContain('The Velero server’s default (30 days unless it was changed)')
    expect(document.body.textContent).not.toContain('0 hours')
  })

  it('shows what will be backed up, and binds the backup to the schedule reviewed', () => {
    render(<VeleroBackupNow schedule={nightly} />)
    act(() => buttonNamed('Back up now')!.click())
    const dialog = document.body.textContent ?? ''
    expect(dialog).toContain('payments')
    expect(dialog).toContain('primary')
    expect(dialog).toContain('7 days')
    expect(dialog).toContain("The schedule's next run waits while this backup is queued or running.")
    const confirm = [...document.querySelectorAll('button')].filter((b) => b.textContent?.trim() === 'Back up now').at(-1)!
    act(() => confirm.click())
    expect(state.mutate).toHaveBeenCalledWith(
      expect.objectContaining({ namespace: 'velero', name: 'nightly', resourceVersion: '42', backupName: expect.stringMatching(/^nightly-\d{14}$/), submission: expect.stringMatching(/^[A-Za-z0-9-]{8,64}$/) }),
      expect.anything(),
    )
  })

  it('keeps the schedule it showed, and refuses one that changed while it was open', () => {
    const root = render(<VeleroBackupNow schedule={nightly} />)
    act(() => buttonNamed('Back up now')!.click())
    const edited = { ...nightly, metadata: { ...nightly.metadata, resourceVersion: '43' }, spec: { ...nightly.spec, template: { ...nightly.spec.template, storageLocation: 'elsewhere' } } }
    act(() => root.render(<VeleroBackupNow schedule={edited} />))
    const text = document.body.textContent ?? ''
    expect(text).toContain('primary')
    expect(text).not.toContain('elsewhere')
    expect(text).toContain('This schedule changed while this was open')
    const confirm = [...document.querySelectorAll('button')].filter((b) => b.textContent?.trim() === 'Back up now').at(-1)!
    expect(confirm.disabled).toBe(true)
  })

  it('cannot start a backup that would fail', () => {
    state.reading = reading({ name: 'nightly', state: 'cannotRun', attention: true })
    render(<VeleroBackupNow schedule={nightly} />)
    expect(buttonNamed('Back up now')!.disabled).toBe(true)
  })

  it('says the backup was created, with a way to open it', () => {
    state.mutate.mockImplementation((_vars: unknown, opts: { onSuccess: (b: unknown) => void }) => opts.onSuccess({ namespace: 'velero', name: 'nightly-20261115210509' }))
    const onNavigate = vi.fn()
    render(<VeleroBackupNow schedule={nightly} onNavigate={onNavigate} />)
    act(() => buttonNamed('Back up now')!.click())
    const confirm = [...document.querySelectorAll('button')].filter((b) => b.textContent?.trim() === 'Back up now').at(-1)!
    act(() => confirm.click())
    expect(state.toast).toHaveBeenCalledWith('Backup nightly-20261115210509 created', undefined, expect.objectContaining({ label: 'Open' }))
    state.toast.mock.calls[0][2].onClick()
    expect(onNavigate).toHaveBeenCalledWith({ kind: 'Backup', group: 'velero.io', namespace: 'velero', name: 'nightly-20261115210509' })
  })

  // A stopped server would only queue it, so "now" would not be true.
  it('is unavailable while the Velero server is stopped, whatever the schedule reads', () => {
    state.reading = reading({ name: 'nightly', state: 'due' })
    state.serverStopped = true
    render(<VeleroBackupNow schedule={nightly} />)
    expect(buttonNamed('Back up now')!.disabled).toBe(true)
  })

  it('says when resource or label filters narrow what it backs up', () => {
    const filtered = { ...nightly, spec: { ...nightly.spec, template: { ...nightly.spec.template, excludedNamespaces: ['kube-system'], labelSelector: { matchLabels: { app: 'api' } } } } }
    render(<VeleroBackupNow schedule={filtered} />)
    act(() => buttonNamed('Back up now')!.click())
    const dialog = document.body.textContent ?? ''
    expect(dialog).toContain('payments, except kube-system')
    expect(dialog).toContain('narrowed by resource or label filters')
  })

  it('is unavailable when Velero runs restore-only', () => {
    state.backupsDisabled = true
    render(<VeleroBackupNow schedule={nightly} />)
    expect(buttonNamed('Back up now')!.disabled).toBe(true)
  })

  it('is absent on a Radar without the action', () => {
    state.support = 'unsupported'
    render(<VeleroBackupNow schedule={nightly} />)
    expect(buttonNamed('Back up now')).toBeUndefined()
  })

  // The server can stop while the confirmation is open.
  it('refuses to confirm once the server has stopped', () => {
    state.reading = reading({ name: 'nightly', state: 'onSchedule' })
    const root = render(<VeleroBackupNow schedule={nightly} />)
    act(() => buttonNamed('Back up now')!.click())
    state.serverStopped = true
    act(() => root.render(<VeleroBackupNow schedule={{ ...nightly }} />))
    const confirm = [...document.querySelectorAll('button')].filter((b) => b.textContent?.trim() === 'Back up now').at(-1)!
    expect(confirm.disabled).toBe(true)
    expect(document.body.textContent).toContain('Start it first')
  })
})

