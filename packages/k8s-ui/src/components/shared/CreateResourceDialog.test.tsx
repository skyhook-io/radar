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
    await act(async () => root.render(<CreateResourceDialog open onClose={onClose} onBack={onBack} backLabel="Back to setup" initialYaml="kind: Cluster" initialMode="create" lockMode onApply={vi.fn()} isApplying={false} onPreview={onPreview} />))
    const draft = 'kind: Cluster\nspec:\n  storage:\n    pvcTemplate:\n      volumeMode: Block\n'
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
    await act(async () => root.render(<CreateResourceDialog open onClose={onClose} onBack={onBack} backLabel="Back to setup" initialYaml="kind: Cluster" onApply={vi.fn()} isApplying />))
    expect(button('Back to setup')!.disabled).toBe(true)
    act(() => root.unmount())
  })

  it('stays strict create after a partial create, with no Apply or Force', async () => {
    const onPreview = vi.fn(async () => ({ documents: [], nonAtomic: true }))
    const onApply = vi.fn(async () => {
      throw Object.assign(new Error('second document failed'), { appliedResults: [{ kind: 'Cluster', name: 'pg-restore' }] })
    })
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)
    await act(async () => {
      root.render(
        <CreateResourceDialog open onClose={() => {}} initialYaml={'kind: Cluster\n---\nkind: Secret\n'} initialMode="create" lockMode onApply={onApply} isApplying={false} onPreview={onPreview} />,
      )
    })
    expect(document.querySelector('[aria-label="Apply mode"]')).toBeNull()

    await act(async () => button('Review')!.click())
    await act(async () => button('Create reviewed resources')!.click())

    expect(document.body.textContent).toContain('This dialog only creates')
    expect(document.querySelector('[aria-label="Apply mode"]')).toBeNull()
    expect(document.body.textContent).not.toContain('Force')

    await act(async () => button('Review')!.click())
    expect(onPreview).toHaveBeenLastCalledWith(expect.objectContaining({ mode: 'create', force: false }))
    act(() => root.unmount())
  })
})
