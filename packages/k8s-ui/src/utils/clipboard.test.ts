import { describe, expect, it, vi } from 'vitest'

import { copyText } from './clipboard'

function fallbackDocument(copied: boolean) {
  const document = {
    createElement: vi.fn(() => textarea),
    body: { appendChild: vi.fn() },
    execCommand: vi.fn(() => copied),
    activeElement: null as unknown,
  }
  const textarea = {
    value: '',
    style: {} as CSSStyleDeclaration,
    setAttribute: vi.fn(),
    select: vi.fn(() => { document.activeElement = textarea }),
    setSelectionRange: vi.fn(),
    remove: vi.fn(),
  }
  return { document, textarea }
}

describe('copyText', () => {
  it('uses the synchronous fallback when the Clipboard API is unavailable on HTTP', async () => {
    const { document } = fallbackDocument(true)

    await expect(copyText('busybox', undefined, document as unknown as Document)).resolves.toBe(true)

    expect(document.execCommand).toHaveBeenCalledWith('copy')
  })

  it('falls back to a synchronous copy command when the Clipboard API rejects', async () => {
    const clipboard = { writeText: vi.fn().mockRejectedValue(new DOMException('Blocked', 'NotAllowedError')) }
    const { document, textarea } = fallbackDocument(true)

    await expect(copyText('busybox', clipboard, document as unknown as Document)).resolves.toBe(true)

    expect(clipboard.writeText).toHaveBeenCalledWith('busybox')
    expect(document.body.appendChild).toHaveBeenCalledWith(textarea)
    expect(textarea.value).toBe('busybox')
    expect(textarea.select).toHaveBeenCalledOnce()
    expect(document.execCommand).toHaveBeenCalledWith('copy')
    expect(textarea.remove).toHaveBeenCalledOnce()
  })

  it('restores focus to the previously active element without scrolling', async () => {
    const { document } = fallbackDocument(true)
    const previouslyFocused = { focus: vi.fn(), isConnected: true }
    document.activeElement = previouslyFocused

    await expect(copyText('busybox', undefined, document as unknown as Document)).resolves.toBe(true)

    expect(previouslyFocused.focus).toHaveBeenCalledWith({ preventScroll: true })
  })

  it('leaves focus alone when a copy handler focused something else', async () => {
    const { document, textarea } = fallbackDocument(true)
    const previouslyFocused = { focus: vi.fn(), isConnected: true }
    document.activeElement = previouslyFocused
    const somethingElse = {}
    textarea.select.mockImplementation(() => { document.activeElement = somethingElse })

    await expect(copyText('busybox', undefined, document as unknown as Document)).resolves.toBe(true)

    expect(previouslyFocused.focus).not.toHaveBeenCalled()
  })

  it('does not refocus an element that left the DOM during the copy', async () => {
    const { document } = fallbackDocument(true)
    const previouslyFocused = { focus: vi.fn(), isConnected: false }
    document.activeElement = previouslyFocused

    await expect(copyText('busybox', undefined, document as unknown as Document)).resolves.toBe(true)

    expect(previouslyFocused.focus).not.toHaveBeenCalled()
  })

  it('does not create a fallback element when the Clipboard API succeeds', async () => {
    const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) }
    const { document } = fallbackDocument(true)

    await expect(copyText('busybox', clipboard, document as unknown as Document)).resolves.toBe(true)

    expect(document.createElement).not.toHaveBeenCalled()
  })
})
