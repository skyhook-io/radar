import { describe, expect, it, vi } from 'vitest'

import { copyText } from './clipboard'

function fallbackDocument(copied: boolean) {
  const textarea = {
    value: '',
    style: {} as CSSStyleDeclaration,
    setAttribute: vi.fn(),
    select: vi.fn(),
    setSelectionRange: vi.fn(),
    remove: vi.fn(),
  }
  const document = {
    createElement: vi.fn(() => textarea),
    body: { appendChild: vi.fn() },
    execCommand: vi.fn(() => copied),
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

  it('does not create a fallback element when the Clipboard API succeeds', async () => {
    const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) }
    const { document } = fallbackDocument(true)

    await expect(copyText('busybox', clipboard, document as unknown as Document)).resolves.toBe(true)

    expect(document.createElement).not.toHaveBeenCalled()
  })
})
