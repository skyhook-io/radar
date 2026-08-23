type ClipboardWriter = Pick<Clipboard, 'writeText'>

/**
 * Copy text from a user gesture, including pages served from an insecure origin.
 * The async Clipboard API is unavailable or rejects on plain HTTP, while the
 * legacy command remains permitted when it runs synchronously from the click.
 */
export async function copyText(
  text: string,
  clipboard: ClipboardWriter | undefined = typeof navigator === 'undefined' ? undefined : navigator.clipboard,
  doc: Document | undefined = typeof document === 'undefined' ? undefined : document,
): Promise<boolean> {
  if (clipboard?.writeText) {
    try {
      await clipboard.writeText(text)
      return true
    } catch {
      // Fall through for insecure origins and denied Clipboard API access.
    }
  }

  if (!doc?.body) return false

  const previouslyFocused = doc.activeElement as { focus?: (options?: FocusOptions) => void; isConnected?: boolean } | null
  const textarea = doc.createElement('textarea')
  textarea.value = text
  textarea.readOnly = true
  textarea.setAttribute('aria-hidden', 'true')
  textarea.style.position = 'fixed'
  textarea.style.opacity = '0'
  textarea.style.pointerEvents = 'none'

  try {
    doc.body.appendChild(textarea)
    textarea.select()
    textarea.setSelectionRange(0, text.length)
    return doc.execCommand('copy')
  } catch {
    return false
  } finally {
    // Restore focus only when the textarea still owns it — a copy handler may
    // have legitimately focused something else — and without scrolling a
    // now-offscreen element back into view.
    const textareaOwnsFocus = doc.activeElement === (textarea as unknown as Element)
    textarea.remove()
    if (textareaOwnsFocus && previouslyFocused?.isConnected !== false) {
      previouslyFocused?.focus?.({ preventScroll: true })
    }
  }
}
