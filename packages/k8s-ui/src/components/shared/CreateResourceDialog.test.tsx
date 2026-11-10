// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('../ui/YamlEditor', () => ({
  YamlEditor: ({ value, onChange }: { value: string; onChange: (value: string) => void }) => <textarea value={value} onChange={(event) => onChange(event.target.value)} />,
}))
vi.mock('../ui/YamlReview', () => ({
  reviewedResourceVersionsForPreview: () => ({}),
  YamlReview: ({ onApply, onBack, applyLabel }: { onApply: () => void; onBack: () => void; applyLabel: string }) => (
    <><button type="button" onClick={onApply}>{applyLabel}</button><button type="button" onClick={onBack}>Back to YAML</button></>
  ),
}))

const { CreateResourceDialog } = await import('./CreateResourceDialog')

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const button = (text: string) => [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === text)

afterEach(() => {
  document.body.innerHTML = ''
})

describe('CreateResourceDialog lockMode', () => {
  it('returns the edited draft after backing out of review, without canceling the parent flow', async () => {
    const onBack = vi.fn()
    const onClose = vi.fn()
    const onPreview = vi.fn(async () => ({ documents: [], nonAtomic: false }))
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)
    await act(async () => root.render(<CreateResourceDialog open onClose={onClose} onBack={onBack} backLabel="Back to setup" initialYaml="kind: ConfigMap" initialMode="create" lockMode onApply={vi.fn()} isApplying={false} onPreview={onPreview} />))
    const draft = 'kind: ConfigMap\nspec:\n  storage:\n    pvcTemplate:\n      volumeMode: Block\n'
    act(() => {
      const editor = document.querySelector('textarea')!
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(editor, draft)
      editor.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => button('Review')!.click())
    expect(onPreview).toHaveBeenCalledWith({ yaml: draft, mode: 'create', force: false })
    act(() => button('Back to YAML')!.click())
    expect(document.querySelector('textarea')!.value).toBe(draft)
    act(() => button('Back to setup')!.click())
    expect(onBack).toHaveBeenCalledWith(draft)
    expect(onClose).not.toHaveBeenCalled()
    await act(async () => root.render(<CreateResourceDialog open onClose={onClose} onBack={onBack} backLabel="Back to setup" initialYaml="kind: ConfigMap" onApply={vi.fn()} isApplying />))
    expect(button('Back to setup')!.disabled).toBe(true)
    act(() => root.unmount())
  })

  it('stays strict create after a partial create, with no Apply or Force', async () => {
    const onPreview = vi.fn(async () => ({ documents: [], nonAtomic: true }))
    const onApply = vi.fn(async () => {
      throw Object.assign(new Error('document 2: failed to create resource: configmaps "second" already exists'), { appliedResults: [{ kind: 'ConfigMap', name: 'settings' }] })
    })
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)
    await act(async () => {
      root.render(
        <CreateResourceDialog open onClose={() => {}} initialYaml={'kind: ConfigMap\n---\nkind: Service\n'} initialMode="create" lockMode onApply={onApply} isApplying={false} onPreview={onPreview} />,
      )
    })
    expect(document.querySelector('[aria-label="Apply mode"]')).toBeNull()

    await act(async () => button('Review')!.click())
    await act(async () => button('Create reviewed resources')!.click())

    expect(document.body.textContent).toContain('This dialog only creates')
    expect(document.body.textContent).toContain('ConfigMap settings')
    expect(document.querySelector('[aria-label="Apply mode"]')).toBeNull()
    expect(document.body.textContent).not.toContain('Force')
    expect(document.body.textContent).not.toContain('Switch to Apply mode')
    const recovery = [...document.querySelectorAll('p')].find(p => p.textContent?.includes('Created before it stopped'))!
    expect(recovery.closest('[inert]')).toBeNull()

    await act(async () => button('Review')!.click())
    expect(onPreview).toHaveBeenLastCalledWith(expect.objectContaining({ mode: 'create', force: false }))
    act(() => root.unmount())
  })
})


it.each([false, true])('returns the exact submitted YAML after a successful write (review=%s)', async (reviewed) => {
  const result = { kind: 'ConfigMap', name: 'settings', namespace: 'app', created: true }
  const onCreated = vi.fn()
  const onApply = vi.fn(async () => [result])
  const onPreview = reviewed ? vi.fn(async () => ({ documents: [], nonAtomic: false, context: 'demo' })) : undefined
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  const submitted = 'kind: ConfigMap\nmetadata:\n  name: settings\n'
  await act(async () => root.render(<CreateResourceDialog open onClose={() => {}} initialYaml={submitted} initialMode="create" lockMode onApply={onApply} isApplying={false} onPreview={onPreview} onCreated={onCreated} />))
  await act(async () => button(reviewed ? 'Review' : 'Create')!.click())
  if (reviewed) await act(async () => button('Create reviewed resources')!.click())
  expect(onApply).toHaveBeenCalledWith(expect.objectContaining({ yaml: submitted, mode: 'create', dryRun: false }))
  expect(onCreated).toHaveBeenCalledWith(result, submitted)
  act(() => root.unmount())
})


it('keeps ordinary Apply, Force and unlocked partial-create recovery', async () => {
  const onPreview = vi.fn(async () => ({ documents: [], nonAtomic: true }))
  const onApply = vi.fn(async () => { throw Object.assign(new Error('document 2: failed to create resource: configmaps "second" already exists'), { appliedResults: [{ kind: 'ConfigMap', name: 'first' }] }) })
  const host = document.createElement('div'); document.body.appendChild(host)
  const root = createRoot(host)
  await act(async () => root.render(<CreateResourceDialog open onClose={() => {}} initialYaml="kind: ConfigMap" onApply={onApply} isApplying={false} onPreview={onPreview} />))
  expect(document.querySelector('[role="radio"][aria-checked="true"]')?.textContent).toBe('apply')
  const force = document.querySelector<HTMLInputElement>('input[type="checkbox"]')!
  await act(async () => force.click())
  await act(async () => button('Review')!.click())
  expect(onPreview).toHaveBeenLastCalledWith(expect.objectContaining({ mode: 'apply', force: true }))
  await act(async () => button('Back to YAML')!.click())
  await act(async () => button('create')!.click())
  await act(async () => button('Review')!.click())
  await act(async () => button('Create reviewed resources')!.click())
  expect(document.querySelector('[role="radio"][aria-checked="true"]')?.textContent).toBe('apply')
  expect(document.body.textContent).toContain('Radar switched to Apply mode')
  expect(document.body.textContent).not.toContain('Change the name to create a new resource')
  expect(document.body.textContent).not.toContain('remove this document if it was already created')
  expect(document.body.textContent).not.toContain('Switch to Apply mode')
  expect(document.body.textContent).toContain('Force')
  await act(async () => root.unmount())
})

it('does not close or notify the parent for a dry run', async () => {
  const onClose = vi.fn(), onCreated = vi.fn(), onApply = vi.fn(async () => [{ kind: 'ConfigMap', namespace: 'app', name: 'settings', created: true }])
  const host = document.createElement('div'); document.body.appendChild(host)
  const root = createRoot(host)
  await act(async () => root.render(<CreateResourceDialog open onClose={onClose} initialYaml="kind: ConfigMap" onApply={onApply} isApplying={false} onCreated={onCreated} />))
  await act(async () => document.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click())
  await act(async () => button('Apply')!.click())
  expect(onApply).toHaveBeenCalledWith(expect.objectContaining({ dryRun: true }))
  expect(onClose).not.toHaveBeenCalled(); expect(onCreated).not.toHaveBeenCalled()
  expect(document.body.textContent).toContain('(dry run)')
  await act(async () => root.unmount())
})

it('closes before delivering the actual submitted draft and retains newer editor text only locally', async () => {
  const calls: string[] = []
  let complete!: (value: { kind: string; namespace: string; name: string; created: boolean; uid: string }[]) => void
  const onApply = vi.fn(() => new Promise<{ kind: string; namespace: string; name: string; created: boolean; uid: string }[]>(resolve => { complete = resolve }))
  const onCreated = vi.fn(() => calls.push('created'))
  const host = document.createElement('div'); document.body.appendChild(host)
  const root = createRoot(host)
  const original = 'kind: ConfigMap\nmetadata:\n  name: original\n'
  await act(async () => root.render(<CreateResourceDialog open onClose={() => calls.push('closed')} initialYaml={original} onApply={onApply} isApplying={false} onCreated={onCreated} />))
  await act(async () => button('Apply')!.click())
  const editor = document.querySelector('textarea')!
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(editor, 'kind: Service')
    editor.dispatchEvent(new Event('input', { bubbles: true }))
  })
  const result = { kind: 'ConfigMap', namespace: 'app', name: 'original', created: true, uid: 'persisted-uid' }
  await act(async () => complete([result]))
  expect(calls).toEqual(['closed', 'created']); expect(onCreated).toHaveBeenCalledWith(result, original)
  await act(async () => root.unmount())
})

it('resets to the selected initial mode on reopen and cancel deliberately abandons the dialog', async () => {
  const onBack = vi.fn(), onClose = vi.fn()
  const host = document.createElement('div'); document.body.appendChild(host)
  const root = createRoot(host)
  const props = { open: true, initialYaml: 'kind: ConfigMap', onClose, onBack, onApply: vi.fn(), isApplying: false }
  await act(async () => root.render(<CreateResourceDialog {...props} />))
  await act(async () => root.render(<CreateResourceDialog {...props} initialMode="create" lockMode />))
  expect(button('Create')).toBeDefined(); expect(document.querySelector('[aria-label="Apply mode"]')).toBeNull()
  await act(async () => button('Cancel')!.click())
  expect(onClose).toHaveBeenCalledOnce(); expect(onBack).not.toHaveBeenCalled()
  await act(async () => root.unmount())
})


it.each(['back', 'reopen', 'cancel'])('retires a successful-write close timer when the user chooses %s', async choice => {
  vi.useFakeTimers()
  const onClose = vi.fn(), onBack = vi.fn()
  const host = document.createElement('div'); document.body.appendChild(host); const root = createRoot(host)
  const props = { open: true, initialYaml: 'kind: ConfigMap', onClose, onBack, onApply: vi.fn(async () => [{ kind: 'ConfigMap', namespace: 'app', name: 'settings', created: true }]), isApplying: false }
  try {
    await act(async () => root.render(<CreateResourceDialog {...props} />))
    await act(async () => button('Apply')!.click())
    if (choice === 'back') await act(async () => button('Back')!.click())
    else if (choice === 'cancel') await act(async () => button('Cancel')!.click())
    else {
      await act(async () => root.render(<CreateResourceDialog {...props} open={false} />))
      await act(async () => root.render(<CreateResourceDialog {...props} />))
    }
    await act(async () => vi.advanceTimersByTime(1200))
    expect(onClose).toHaveBeenCalledTimes(choice === 'cancel' ? 1 : 0)
    expect(onBack).toHaveBeenCalledTimes(choice === 'back' ? 1 : 0)
  } finally { await act(async () => root.unmount()); host.remove(); vi.useRealTimers() }
})


it.each([false, true])('shows real partial-create recovery without a preview (locked=%s)', async lockMode => {
  const onApply = vi.fn(async () => { throw Object.assign(new Error('document 2: configmaps "second" already exists'), { appliedResults: [{ kind: 'ConfigMap', namespace: 'app', name: 'first', uid: 'real-first-uid' }] }) })
  const onCreated = vi.fn()
  const host = document.createElement('div'); document.body.appendChild(host); const root = createRoot(host)
  try {
    await act(async () => root.render(<CreateResourceDialog open onClose={() => {}} initialYaml="kind: ConfigMap" initialMode="create" lockMode={lockMode} onApply={onApply} isApplying={false} onCreated={onCreated} />))
    await act(async () => button('Create')!.click())
    expect(document.body.textContent).toContain('Created before it stopped: ConfigMap app/first.')
    expect(onCreated).not.toHaveBeenCalled()
    expect(document.body.textContent).not.toContain('Switch to Apply mode')
    expect(document.body.textContent).not.toContain('Radar switched to Apply mode')
    expect(document.body.textContent).toContain('Remove the created objects from the manifest before retrying Create.')
    expect(document.body.textContent).toContain('Change the name to create a new resource')
    if (lockMode) {
      expect(document.querySelector('[aria-label="Apply mode"]')).toBeNull()
      expect(document.body.textContent).toContain('This dialog only creates')
    } else {
      expect(document.querySelector('[role="radio"][aria-checked="true"]')?.textContent).toBe('create')
    }
    await act(async () => button('Create')!.click())
    expect(onApply).toHaveBeenLastCalledWith(expect.objectContaining({ mode: 'create', dryRun: false }))
    if (!lockMode) {
      await act(async () => button('apply')!.click())
      await act(async () => button('Apply')!.click())
      expect(onApply).toHaveBeenLastCalledWith(expect.objectContaining({ mode: 'apply', dryRun: false }))
    }
  } finally { await act(async () => root.unmount()); host.remove() }
})

it.each([false, true])('retains and accumulates real partial-create results across retries and review Back (review=%s)', async reviewed => {
  const first = { kind: 'ConfigMap', namespace: 'app', name: 'first', uid: 'first-uid', created: true }
  const later = { kind: 'Service', namespace: 'app', name: 'later', uid: 'later-uid', created: true }
  const onApply = vi.fn()
    .mockRejectedValueOnce(Object.assign(new Error('document 2: configmaps "collision" already exists'), { appliedResults: [first] }))
    .mockRejectedValueOnce(new Error('document 1: configmaps "first" already exists'))
    .mockRejectedValueOnce(Object.assign(new Error('document 2: configmaps "collision" already exists'), { appliedResults: [later] }))
  const onPreview = reviewed ? vi.fn(async () => ({ documents: [], nonAtomic: true })) : undefined
  const host = document.createElement('div'); document.body.appendChild(host); const root = createRoot(host)
  try {
    await act(async () => root.render(<CreateResourceDialog open onClose={() => {}} initialYaml="kind: ConfigMap" initialMode="create" lockMode onApply={onApply} isApplying={false} onPreview={onPreview} />))
    const submit = async () => {
      await act(async () => button(reviewed ? 'Review' : 'Create')!.click())
      if (reviewed) await act(async () => button('Create reviewed resources')!.click())
    }
    await submit()
    expect(document.body.textContent).toContain('Created before it stopped: ConfigMap app/first.')
    if (reviewed) {
      await act(async () => button('Review')!.click())
      await act(async () => button('Back to YAML')!.click())
      expect(document.body.textContent).toContain('Created before it stopped: ConfigMap app/first.')
      expect(document.body.textContent).not.toContain('Preview failed')
    }
    await submit()
    if (reviewed) await act(async () => button('Back to YAML')!.click())
    expect(document.body.textContent).toContain('Created before it stopped: ConfigMap app/first.')
    await submit()
    expect(document.body.textContent).toContain('Created before it stopped: ConfigMap app/first, Service app/later.')
    expect(onApply).toHaveBeenCalledTimes(3)
  } finally { await act(async () => root.unmount()); host.remove() }
})

it('keeps partial-create history after dry-run success and clears it after a completed write', async () => {
  const created = { kind: 'ConfigMap', namespace: 'app', name: 'first', uid: 'first-uid', created: true }
  const onApply = vi.fn()
    .mockRejectedValueOnce(Object.assign(new Error('document 2: configmaps "collision" already exists'), { appliedResults: [created] }))
    .mockResolvedValueOnce([created])
    .mockResolvedValueOnce([created])
  const host = document.createElement('div'); document.body.appendChild(host); const root = createRoot(host)
  try {
    await act(async () => root.render(<CreateResourceDialog open onClose={() => {}} initialYaml="kind: ConfigMap" initialMode="create" lockMode onApply={onApply} isApplying={false} />))
    await act(async () => button('Create')!.click())
    const dryRun = document.querySelector<HTMLInputElement>('input[type="checkbox"]')!
    await act(async () => dryRun.click())
    await act(async () => button('Create')!.click())
    expect(onApply).toHaveBeenLastCalledWith(expect.objectContaining({ dryRun: true }))
    expect(document.body.textContent).toContain('(dry run)')
    expect(document.body.textContent).toContain('Created before it stopped: ConfigMap app/first.')
    await act(async () => dryRun.click())
    await act(async () => button('Create')!.click())
    expect(onApply).toHaveBeenLastCalledWith(expect.objectContaining({ dryRun: false }))
    expect(document.body.textContent).not.toContain('Created before it stopped')
  } finally { await act(async () => root.unmount()); host.remove() }
})

it('adds only confirmed creations from an Apply recovery attempt to earlier partial-create facts', async () => {
  const first = { kind: 'ConfigMap', namespace: 'app', name: 'first', uid: 'first-uid', created: true }
  const updated = { kind: 'Service', namespace: 'app', name: 'updated', uid: 'updated-uid', created: false }
  const later = { kind: 'Service', namespace: 'app', name: 'later', uid: 'later-uid', created: true }
  const onApply = vi.fn()
    .mockRejectedValueOnce(Object.assign(new Error('document 2 failed'), { appliedResults: [first] }))
    .mockRejectedValueOnce(Object.assign(new Error('document 3 failed'), { appliedResults: [updated, later] }))
  const onPreview = vi.fn(async () => ({ documents: [], nonAtomic: true }))
  const host = document.createElement('div'); document.body.appendChild(host); const root = createRoot(host)
  try {
    await act(async () => root.render(<CreateResourceDialog open onClose={() => {}} initialYaml="kind: ConfigMap" initialMode="create" onApply={onApply} isApplying={false} onPreview={onPreview} />))
    await act(async () => button('Review')!.click())
    await act(async () => button('Create reviewed resources')!.click())
    expect(document.querySelector('[role="radio"][aria-checked="true"]')?.textContent).toBe('apply')
    await act(async () => button('Review')!.click())
    await act(async () => button('Apply reviewed changes')!.click())
    expect(onApply).toHaveBeenLastCalledWith(expect.objectContaining({ mode: 'apply' }))
    expect(document.body.textContent).toContain('Created before it stopped: ConfigMap app/first, Service app/later.')
    expect(document.body.textContent).not.toContain('Service app/updated')
  } finally { await act(async () => root.unmount()); host.remove() }
})

it('keeps created-object facts and the edited draft when canceling a file replacement', async () => {
  const first = { kind: 'ConfigMap', namespace: 'app', name: 'first', uid: 'first-uid', created: true }
  const onApply = vi.fn(async () => { throw Object.assign(new Error('document 2 failed'), { appliedResults: [first] }) })
  const onClose = vi.fn()
  const host = document.createElement('div'); document.body.appendChild(host); const root = createRoot(host)
  const edited = 'kind: ConfigMap\nmetadata:\n  name: first\n'
  try {
    await act(async () => root.render(<CreateResourceDialog open onClose={onClose} initialYaml="kind: ConfigMap" initialMode="create" lockMode onApply={onApply} isApplying={false} />))
    const editor = document.querySelector('textarea')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(editor, edited)
      editor.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => button('Create')!.click())
    expect(document.body.textContent).toContain('Created before it stopped: ConfigMap app/first.')
    const file = new File(['kind: Service'], 'replacement.yaml', { type: 'text/yaml' })
    Object.defineProperty(file, 'text', { value: async () => 'kind: Service' })
    const input = document.querySelector<HTMLInputElement>('input[type="file"]')!
    Object.defineProperty(input, 'files', { value: [file], configurable: true })
    await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })))
    const confirmation = [...document.querySelectorAll('h3')].find(heading => heading.textContent === 'Replace editor contents?')!.closest('[role="dialog"]')!
    const cancel = [...confirmation.querySelectorAll('button')].find(control => control.textContent?.trim() === 'Cancel')!
    await act(async () => cancel.click())
    expect(document.querySelector('textarea')!.value).toBe(edited)
    expect(document.body.textContent).toContain('Created before it stopped: ConfigMap app/first.')
    expect(onClose).not.toHaveBeenCalled()
  } finally { await act(async () => root.unmount()); host.remove() }
})

it.each(['apply', 'create'] as const)('explains unknown live pause only in direct %s mode without changing submission', async (mode) => {
  const onApply = vi.fn(async () => [])
  const host = document.createElement('div'); document.body.appendChild(host); const root = createRoot(host)
  const yaml = 'apiVersion: example.test/v1\nkind: Bucket\nmetadata:\n  name: archive\nspec:\n  providerConfigRef:\n    name: default\n'
  try {
    await act(async () => root.render(<CreateResourceDialog open onClose={() => {}} initialYaml={yaml} initialMode={mode} lockMode={mode === 'create'} onApply={onApply} isApplying={false} />))
    expect(document.body.textContent?.includes('Existing pause state is unknown')).toBe(mode === 'apply')
    const submit = button(mode === 'apply' ? 'Apply' : 'Create')!
    expect(submit.disabled).toBe(false)
    await act(async () => submit.click())
    expect(onApply).toHaveBeenCalledWith(expect.objectContaining({ yaml, mode }))
  } finally { await act(async () => root.unmount()); host.remove() }
})
