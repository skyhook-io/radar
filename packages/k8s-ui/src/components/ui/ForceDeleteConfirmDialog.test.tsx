// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FINALIZER_ORPHAN_WARNING, ForceDeleteConfirmDialog, forceDeleteCopy } from './ForceDeleteConfirmDialog'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

describe('forceDeleteCopy', () => {
  it('does not mention finalizers when the resource has none', () => {
    const copy = forceDeleteCopy([], false, true)
    expect(copy.checkboxLabel).toBe('Force delete (no grace period)')
    expect(copy.finalizerNote).toBeNull()
    expect(copy.forceNote).toBeNull()
  })

  it('on a live resource, names the finalizers and says force will not remove them yet', () => {
    // Force used to strip finalizers from any resource, skipping cleanup such
    // as a Karpenter node terminating its cloud instance.
    const copy = forceDeleteCopy(['karpenter.sh/termination'], false, true)
    expect(copy.finalizerNote).toContain('karpenter.sh/termination')
    expect(copy.checkboxLabel).not.toMatch(/remove finalizers|strips finalizers/i)
    expect(copy.forceNote).toEqual({ tone: 'info', text: expect.stringMatching(/does not remove finalizers/) })
  })

  it('on a terminating resource, names the finalizers and warns what removing them leaves behind', () => {
    const copy = forceDeleteCopy(['example.com/hold', 'example.com/other'], true, true)
    expect(copy.finalizerNote).toContain('example.com/hold, example.com/other')
    expect(copy.checkboxLabel).toBe('Force delete (remove finalizers)')
    expect(copy.forceNote).toEqual({ tone: 'warning', text: FINALIZER_ORPHAN_WARNING })
  })
})

describe('forceDeleteCopy against a Radar that removes finalizers on every force delete', () => {
  // A newer UI can be embedded against an older Radar. That Radar still
  // removes finalizers from a live resource, so the dialog must not promise
  // otherwise.
  it('warns about orphaned resources on a live resource', () => {
    const copy = forceDeleteCopy(['karpenter.sh/termination'], false, false)
    expect(copy.checkboxLabel).toBe('Force delete (remove finalizers)')
    expect(copy.finalizerNote).toContain('karpenter.sh/termination')
    expect(copy.forceNote).toEqual({ tone: 'warning', text: FINALIZER_ORPHAN_WARNING })
  })

  it('still warns when no finalizers are shown, because it removes whatever is there at delete time', () => {
    const copy = forceDeleteCopy([], false, false)
    expect(copy.checkboxLabel).toBe('Force delete (remove finalizers)')
    expect(copy.forceNote).toEqual({ tone: 'warning', text: FINALIZER_ORPHAN_WARNING })
  })
})

describe('ForceDeleteConfirmDialog', () => {
  async function render(props: { finalizers?: string[]; terminating?: boolean; safeForceDelete?: boolean }) {
    const element = document.createElement('div')
    document.body.appendChild(element)
    const root = createRoot(element)
    const onConfirm = vi.fn()
    await act(async () => {
      root.render(
        <ForceDeleteConfirmDialog
          open
          onClose={() => {}}
          onConfirm={onConfirm}
          resourceName="held"
          resourceKind="ConfigMap"
          namespaceName="ns"
          isLoading={false}
          {...props}
        />,
      )
    })
    return { root, onConfirm }
  }

  it('shows the orphan warning only after force is checked on a terminating resource', async () => {
    const { root } = await render({ finalizers: ['example.com/hold'], terminating: true })
    expect(document.body.textContent).toContain('Waiting on finalizers: example.com/hold')
    expect(document.body.textContent).not.toContain(FINALIZER_ORPHAN_WARNING)

    const checkbox = document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!
    await act(async () => { checkbox.click() })
    expect(document.body.textContent).toContain(FINALIZER_ORPHAN_WARNING)
    expect(document.body.textContent).toContain('Force delete (remove finalizers)')
    await act(async () => root.unmount())
    document.body.innerHTML = ''
  })

  it('never shows the old "strips finalizers" label', async () => {
    const { root } = await render({ finalizers: ['example.com/hold'], terminating: false, safeForceDelete: true })
    expect(document.body.textContent).not.toMatch(/strips finalizers/)
    await act(async () => root.unmount())
    document.body.innerHTML = ''
  })

  it('keeps the orphan warning on a live resource until the host says the server is safe', async () => {
    const { root } = await render({ finalizers: ['example.com/hold'], terminating: false })
    const checkbox = document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!
    await act(async () => { checkbox.click() })
    expect(document.body.textContent).toContain(FINALIZER_ORPHAN_WARNING)
    expect(document.body.textContent).not.toMatch(/does not remove finalizers/)
    await act(async () => root.unmount())
    document.body.innerHTML = ''
  })
})

describe('ForceDeleteConfirmDialog consent to remove finalizers', () => {
  async function confirmForce(props: { finalizers: string[]; terminating: boolean; safeForceDelete: boolean }) {
    const element = document.createElement('div')
    document.body.appendChild(element)
    const root = createRoot(element)
    const onConfirm = vi.fn()
    await act(async () => {
      root.render(
        <ForceDeleteConfirmDialog
          open
          onClose={() => {}}
          onConfirm={onConfirm}
          resourceName="held"
          resourceKind="ConfigMap"
          namespaceName="ns"
          isLoading={false}
          resourceUid="uid-a"
          {...props}
        />,
      )
    })
    await act(async () => { document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click() })
    const button = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent === 'Force Delete')!
    await act(async () => { button.click() })
    await act(async () => root.unmount())
    document.body.innerHTML = ''
    return onConfirm
  }

  it('names the finalizers it warned about when the resource is already being deleted', async () => {
    const onConfirm = await confirmForce({ finalizers: ['example.com/hold'], terminating: true, safeForceDelete: true })
    expect(onConfirm).toHaveBeenCalledWith(true, { uid: 'uid-a', removeFinalizers: ['example.com/hold'] })
  })

  it('sends no consent for a live resource, so a resource that starts terminating meanwhile keeps its finalizers', async () => {
    const onConfirm = await confirmForce({ finalizers: ['example.com/hold'], terminating: false, safeForceDelete: true })
    expect(onConfirm).toHaveBeenCalledWith(true, { uid: 'uid-a', removeFinalizers: undefined })
  })

  it('sends consent for the finalizers it warned about even when the capability is unknown', async () => {
    // The warning is the same either way; an older Radar ignores the consent
    // and removes them as warned, a newer one needs it.
    const onConfirm = await confirmForce({ finalizers: ['example.com/hold'], terminating: true, safeForceDelete: false })
    expect(onConfirm).toHaveBeenCalledWith(true, { uid: 'uid-a', removeFinalizers: ['example.com/hold'] })
  })

  it('never sends consent for a live resource, whatever the capability', async () => {
    const onConfirm = await confirmForce({ finalizers: ['example.com/hold'], terminating: false, safeForceDelete: false })
    expect(onConfirm).toHaveBeenCalledWith(true, { uid: 'uid-a', removeFinalizers: undefined })
  })
})

describe('ForceDeleteConfirmDialog when the resource changes while open', () => {
  const base = {
    open: true,
    onClose: () => {},
    resourceName: 'held',
    resourceKind: 'ConfigMap',
    namespaceName: 'ns',
    isLoading: false,
    safeForceDelete: true,
    resourceUid: 'uid-a',
    finalizers: ['example.com/hold'],
  }
  const changes: Record<string, Partial<typeof base> & { terminating?: boolean }> = {
    'starts terminating': { terminating: true },
    'gets a different finalizer list': { finalizers: ['example.com/hold', 'karpenter.sh/termination'] },
    'is replaced (new UID)': { resourceUid: 'uid-b' },
  }

  for (const [label, change] of Object.entries(changes)) {
    it(`unchecks force and says why when it ${label}`, async () => {
      // Force was checked against what the dialog said before. Keeping it
      // checked would carry that agreement over to something the user has not
      // read, such as removing finalizers.
      const element = document.createElement('div')
      document.body.appendChild(element)
      const root = createRoot(element)
      const onConfirm = vi.fn()
      await act(async () => { root.render(<ForceDeleteConfirmDialog {...base} onConfirm={onConfirm} />) })
      await act(async () => { document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click() })
      await act(async () => { root.render(<ForceDeleteConfirmDialog {...base} {...change} onConfirm={onConfirm} />) })

      expect(document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!.checked).toBe(false)
      expect(document.body.textContent).toContain('so Force delete was unchecked')
      const button = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent === 'Delete')!
      await act(async () => { button.click() })
      expect(onConfirm).toHaveBeenCalledWith(false, expect.objectContaining({ removeFinalizers: undefined }))
      await act(async () => root.unmount())
      document.body.innerHTML = ''
    })
  }
})

describe('ForceDeleteConfirmDialog while its own delete is in flight', () => {
  it('keeps force checked when the resource starts terminating because of that delete', async () => {
    const props = {
      open: true,
      onClose: () => {},
      onConfirm: vi.fn(),
      resourceName: 'held',
      resourceKind: 'ConfigMap',
      namespaceName: 'ns',
      safeForceDelete: true,
      resourceUid: 'uid-a',
      finalizers: ['example.com/hold'],
    }
    const element = document.createElement('div')
    document.body.appendChild(element)
    const root = createRoot(element)
    await act(async () => { root.render(<ForceDeleteConfirmDialog {...props} isLoading={false} />) })
    await act(async () => { document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click() })
    await act(async () => { root.render(<ForceDeleteConfirmDialog {...props} isLoading terminating />) })

    expect(document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!.checked).toBe(true)
    expect(document.body.textContent).not.toContain('so Force delete was unchecked')
    await act(async () => root.unmount())
    document.body.innerHTML = ''
  })

  it('unchecks force once a failed request settles, so a retry does not reuse the old agreement', async () => {
    // Force was checked on a live resource ("does not remove finalizers").
    // The delete started termination but the response failed; the dialog
    // stays open. Confirming again now would remove finalizers.
    const props = {
      open: true,
      onClose: () => {},
      resourceName: 'held',
      resourceKind: 'ConfigMap',
      namespaceName: 'ns',
      safeForceDelete: true,
      resourceUid: 'uid-a',
      finalizers: ['example.com/hold'],
    }
    const onConfirm = vi.fn()
    const element = document.createElement('div')
    document.body.appendChild(element)
    const root = createRoot(element)
    try {
      await act(async () => { root.render(<ForceDeleteConfirmDialog {...props} onConfirm={onConfirm} isLoading={false} />) })
      await act(async () => { document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click() })
      await act(async () => { root.render(<ForceDeleteConfirmDialog {...props} onConfirm={onConfirm} isLoading terminating />) })
      await act(async () => { root.render(<ForceDeleteConfirmDialog {...props} onConfirm={onConfirm} isLoading={false} terminating />) })

      expect(document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!.checked).toBe(false)
      expect(document.body.textContent).toContain('The resource changed while this was open')
      const button = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent === 'Delete')!
      await act(async () => { button.click() })
      expect(onConfirm).toHaveBeenCalledWith(false, expect.objectContaining({ removeFinalizers: undefined }))
    } finally {
      await act(async () => root.unmount())
      document.body.innerHTML = ''
    }
  })
})

describe('ForceDeleteConfirmDialog when the connected Radar changes while open', () => {
  it('keeps force checked when Radar turns out to keep finalizers (capabilities loading late)', async () => {
    const props = {
      open: true,
      onClose: () => {},
      onConfirm: vi.fn(),
      resourceName: 'held',
      resourceKind: 'ConfigMap',
      namespaceName: 'ns',
      isLoading: false,
      resourceUid: 'uid-a',
      finalizers: ['example.com/hold'],
    }
    const element = document.createElement('div')
    document.body.appendChild(element)
    const root = createRoot(element)
    try {
      await act(async () => { root.render(<ForceDeleteConfirmDialog {...props} safeForceDelete={false} />) })
      await act(async () => { document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click() })
      await act(async () => { root.render(<ForceDeleteConfirmDialog {...props} safeForceDelete />) })
      expect(document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!.checked).toBe(true)
      expect(document.body.textContent).not.toContain('changed how force delete works')
    } finally {
      await act(async () => root.unmount())
      document.body.innerHTML = ''
    }
  })

  it('unchecks force when Radar stops keeping finalizers on a live resource', async () => {
    // Force was checked under "does not remove finalizers". An older Radar
    // (after a rollback) removes them, so that agreement no longer holds.
    const base = {
      open: true,
      onClose: () => {},
      resourceName: 'held',
      resourceKind: 'ConfigMap',
      namespaceName: 'ns',
      isLoading: false,
      resourceUid: 'uid-a',
      finalizers: ['example.com/hold'],
      terminating: false,
    }
    const element = document.createElement('div')
    document.body.appendChild(element)
    const root = createRoot(element)
    const onConfirm = vi.fn()
    await act(async () => { root.render(<ForceDeleteConfirmDialog {...base} safeForceDelete onConfirm={onConfirm} />) })
    await act(async () => { document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click() })
    await act(async () => { root.render(<ForceDeleteConfirmDialog {...base} safeForceDelete={false} onConfirm={onConfirm} />) })

    expect(document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!.checked).toBe(false)
    expect(document.body.textContent).toContain('changed how force delete works')
    expect(document.body.textContent).not.toContain('The resource changed while this was open')
    await act(async () => root.unmount())
    document.body.innerHTML = ''
  })
})

describe('Deletion preview states', () => {
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
    expect(base.onConfirm).toHaveBeenCalledWith(false, { uid: undefined, removeFinalizers: undefined })
  })
  it('shows loading without presenting stale failure or success as a result', async () => {
    await act(async () => root.render(<ForceDeleteConfirmDialog {...base} cascadeLoading cascadePreviewFailed cascadeRootResolved />))
    expect(document.body.textContent).toContain('Checking related resources')
    expect(document.body.textContent).not.toContain('unavailable')
    expect(document.body.textContent).not.toContain('does not predict')
  })
})
