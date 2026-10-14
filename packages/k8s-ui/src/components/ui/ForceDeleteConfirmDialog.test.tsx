// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ForceDeleteConfirmDialog } from './ForceDeleteConfirmDialog'

let root: Root
const base = { open: true, onClose: vi.fn(), onConfirm: vi.fn(), resourceName: 'web', resourceKind: 'Deployment', namespaceName: 'demo', isLoading: false }
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
})
afterEach(async () => {
  await act(async () => root.unmount())
  document.body.replaceChildren()
  vi.clearAllMocks()
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
})

describe('Deletion preview states', () => {
  it('states the namespace-wide deletion consequence without relying on topology', async () => {
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} resourceKind="Namespace" resourceName="demo" namespaceName="" cascadeRootResolved={false} />))
    expect(document.body.textContent).toContain('delete the namespace "demo" and the resources in it')
    expect(document.body.textContent).not.toContain("not in Radar's topology")
  })
  it('shows one advisory statement for a resolved preview', async () => {
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved cascadeDependents={[{ kind: 'Pod', namespace: 'demo', name: 'web-pod' }]} />))
    const toggle = [...document.querySelectorAll('button')].find(b => b.textContent?.includes('May affect'))!
    await act(async () => toggle.click())
    expect(document.body.textContent).toContain('web-pod')
    expect(document.body.textContent?.match(/does not predict what will be deleted/g)).toHaveLength(1)
    expect(document.body.textContent).not.toContain('Other resources may be affected')
  })
  it.each([[], undefined])('omits the advisory statement for an empty resolved preview (%j)', async (dependents) => {
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved cascadeDependents={dependents} />))
    expect(document.body.textContent).not.toContain('May affect')
    expect(document.body.textContent).not.toContain('does not predict')
    expect(document.body.textContent).not.toContain('unavailable')
    expect(document.body.textContent).not.toContain('Other resources may be affected')
  })
  it('distinguishes an observed unresolved root from a fetch failure', async () => {
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved={false} />))
    expect(document.body.textContent).toContain("This resource is not in Radar's topology")
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved={false} cascadePreviewFailed cascadeDependents={[{ kind: 'Pod', namespace: 'demo', name: 'stale' }]} />))
    expect(document.body.textContent).toContain('Related-resource preview unavailable.')
    expect(document.body.textContent).not.toContain('This resource is not in')
    expect(document.body.textContent).not.toContain('Other resources may be affected')
    expect(document.body.textContent).not.toContain('May affect')
    const confirm = [...document.querySelectorAll('button')].find(b => b.textContent === 'Delete')!
    expect(confirm.disabled).toBe(false)
    await act(async () => confirm.click())
    expect(base.onConfirm).toHaveBeenCalledWith(false)
  })
  it('shows loading without presenting stale failure or success as a result', async () => {
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeLoading cascadePreviewFailed cascadeRootResolved />))
    expect(document.body.textContent).toContain('Checking related resources')
    expect(document.body.textContent).not.toContain('unavailable')
    expect(document.body.textContent).not.toContain('does not predict')
  })
})
