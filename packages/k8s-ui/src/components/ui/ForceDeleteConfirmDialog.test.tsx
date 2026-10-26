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
    expect(document.body.textContent).toContain('delete the namespace "demo" and all resources in it')
    expect(document.body.textContent).not.toContain("not in Radar's topology")
  })
  it('shows one advisory statement for a resolved preview', async () => {
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved cascadeDependents={[{ kind: 'Pod', namespace: 'demo', name: 'web-pod' }]} />))
    const toggle = [...document.querySelectorAll('button')].find(b => b.textContent?.includes('May also delete'))!
    await act(async () => toggle.click())
    expect(document.body.textContent).toContain('web-pod')
    expect(document.body.textContent).toContain("Kubernetes deletes only those that name this one as their owner")
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
    expect(document.body.textContent).toContain("Radar couldn't verify which dependent resources")
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved={false} cascadePreviewFailed cascadeDependents={[{ kind: 'Pod', namespace: 'demo', name: 'stale' }]} />))
    expect(document.body.textContent).toContain('Deletion preview unavailable')
    expect(document.body.textContent).toContain('Additional resources may be deleted.')
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
    expect(document.body.textContent).toContain('Checking for dependent resources')
    const confirm = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent === 'Delete')!
    expect(confirm.disabled).toBe(true)
    expect(document.body.textContent).not.toContain('unavailable')
    expect(document.body.textContent).not.toContain('does not predict')
  })
})

 it('separates observed collection, uncertainty and partial coverage', async () => {
  await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved cascadeDependents={[{kind:'Pod', namespace:'demo', name:'certain'}]} cascadeDetail={{basis:'ownerReferences', possibleDependents:[{kind:'ConfigMap', namespace:'demo', name:'possible'}], coverage:{complete:false,narrowed:true}}} />))
  expect(document.body.textContent).toContain('Garbage collection is expected to delete 1 dependent resource')
  expect(document.body.textContent).toContain('May also delete 1 resource')
  expect(document.body.textContent).toContain('This preview is partial')
 })
 it('does not promise force deletion can undo controller cleanup', async () => {
  await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeDetail={{controllerTeardown:{controller:'Argo CD', action:'prune', destination:'remote'}}} />))
  const force = document.querySelector('input[type="checkbox"]') as HTMLInputElement
  await act(async () => force.click())
  expect(document.body.textContent).toContain('remaining cleanup may be skipped')
  expect(document.body.textContent).toContain('cannot undo it')
  expect(document.body.textContent).not.toContain('Resources stay')
 })

 it('changes the pending-orphan warning when force is selected', async () => {
  await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeRootResolved cascadeDependents={[{kind:'Pod', namespace:'demo', name:'child'}]} cascadeDetail={{basis:'ownerReferences', propagation:'Orphan'}} />))
  expect(document.body.textContent).toContain('dependents are orphaned')
  expect(document.body.textContent).not.toContain('Garbage collection is expected')
  await act(async () => (document.querySelector('input[type="checkbox"]') as HTMLInputElement).click())
  expect(document.body.textContent).not.toContain('dependents are orphaned')
  expect(document.body.textContent).toContain('Force delete removes the orphan finalizer')
  expect(document.body.textContent).toContain('Garbage collection is expected')
 })
