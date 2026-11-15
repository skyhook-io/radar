// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { schedule as reading } from './velero.fixtures'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const state = vi.hoisted(() => ({ support: 'supported', mutate: vi.fn(), data: undefined as unknown }))
vi.mock('../../api/client', () => ({ useRadarFeature: () => ({ support: state.support }) }))
vi.mock('../../api/velero', () => ({
  useVeleroBackupNow: () => ({ mutate: state.mutate, reset: vi.fn(), isPending: false, data: state.data }),
}))

const { VeleroBackupNow } = await import('./VeleroBackupNow')

const nightly = {
  metadata: { namespace: 'velero', name: 'nightly', resourceVersion: '42' },
  spec: { schedule: '0 1 * * *', template: { includedNamespaces: ['payments'], storageLocation: 'primary', ttl: '168h0m0s' } },
}

let host: HTMLDivElement | undefined
function render(node: React.ReactNode) {
  host = document.createElement('div')
  document.body.append(host)
  const root = createRoot(host)
  act(() => root.render(node))
  return root
}
afterEach(() => {
  host?.remove()
  document.body.innerHTML = ''
  state.support = 'supported'
  state.data = undefined
  state.mutate.mockReset()
})

const buttonNamed = (label: string) => [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label)

describe('Back up now', () => {
  it('shows what will be backed up, and binds the backup to the schedule reviewed', () => {
    render(<VeleroBackupNow schedule={nightly} reading={reading({ name: 'nightly' })} />)
    act(() => buttonNamed('Back up now')!.click())
    const dialog = document.body.textContent ?? ''
    expect(dialog).toContain('payments')
    expect(dialog).toContain('primary')
    expect(dialog).toContain('168h0m0s')
    expect(dialog).toContain("Velero waits for it to finish before submitting the schedule's next run")
    const confirm = [...document.querySelectorAll('button')].filter((b) => b.textContent?.trim() === 'Back up now').at(-1)!
    act(() => confirm.click())
    expect(state.mutate).toHaveBeenCalledWith({ namespace: 'velero', name: 'nightly', resourceVersion: '42' }, expect.anything())
  })

  it('cannot start a backup that would fail', () => {
    render(<VeleroBackupNow schedule={nightly} reading={reading({ name: 'nightly', state: 'cannotRun', attention: true })} />)
    expect(buttonNamed('Back up now')!.disabled).toBe(true)
  })

  it('links the backup it created', () => {
    state.data = { namespace: 'velero', name: 'nightly-20261115210509' }
    const onNavigate = vi.fn()
    render(<VeleroBackupNow schedule={nightly} reading={reading({ name: 'nightly' })} onNavigate={onNavigate} />)
    act(() => buttonNamed('nightly-20261115210509')!.click())
    expect(onNavigate).toHaveBeenCalledWith({ kind: 'Backup', group: 'velero.io', namespace: 'velero', name: 'nightly-20261115210509' })
  })

  it('is absent on a Radar without the action', () => {
    state.support = 'unsupported'
    render(<VeleroBackupNow schedule={nightly} reading={reading({ name: 'nightly' })} />)
    expect(buttonNamed('Back up now')).toBeUndefined()
  })
})
