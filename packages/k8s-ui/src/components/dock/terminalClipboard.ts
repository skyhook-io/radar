import type { Terminal as XTerm } from '@xterm/xterm'
import { isMac } from '../../utils/platform'

export interface PasteConfirmInfo {
  lineCount: number
  text: string
}

/** Asks the host to confirm a risky paste; resolves true to proceed. */
export type PasteConfirmer = (info: PasteConfirmInfo) => Promise<boolean>
export type TerminalCopyOnSelectState = 'on' | 'off' | 'pending'

export interface TerminalClipboardOptions {
  confirmPaste?: PasteConfirmer
  /** Notified when the selection changes; drives the toolbar Copy button's enabled state. */
  onSelectionChange?: (hasSelection: boolean) => void
  copyOnSelect?: () => TerminalCopyOnSelectState
  onCopyError?: () => void
  onCopyPending?: () => void
}

/** Copies the terminal's current selection to the clipboard. No-op if nothing is selected. */
export function copyTerminalSelection(xterm: XTerm, onError?: () => void, onSuccess?: () => void): void {
  const selection = xterm.getSelection()
  if (!selection) return
  if (!navigator.clipboard?.writeText) {
    onError?.()
    return
  }
  navigator.clipboard.writeText(selection).then(() => onSuccess?.(), () => onError?.())
}

function pasteLineCount(text: string): number {
  return text.replace(/\r\n?/g, '\n').replace(/\n+$/, '').split('\n').length
}

/**
 * A paste is risky when it spans multiple lines AND the shell hasn't enabled
 * bracketed-paste mode — without that mode every newline runs as a command, so
 * the paste auto-executes. Mirrors VS Code's default: warn only when the paste
 * would actually run (modern bash/zsh enable bracketed paste; sh/ash/dash don't).
 */
function isRiskyMultilinePaste(xterm: XTerm, text: string): boolean {
  if (xterm.modes.bracketedPasteMode) return false
  return text.replace(/\r\n?/g, '\n').trim().includes('\n')
}

/**
 * Wires terminal clipboard behavior onto an xterm instance.
 *
 * Copy uses the toolbar button or macOS ⌘C. Optional copy-on-select only
 * copies after a primary-button selection gesture, so Find and right-click
 * selection changes cannot overwrite the clipboard before a paste.
 *
 * Paste: right-click on Windows/Linux (PuTTY/VS Code convention); macOS keeps its
 * native menu. A risky multi-line paste is confirmed first via `confirmPaste`;
 * the capture listener covers every paste entry point (Cmd/Ctrl+V, the macOS
 * native menu) and the right-click handler reuses the gate. readText() is blocked
 * in the Wails desktop webview, so all ops fail safe. Returns a disposer.
 */
export function setupTerminalClipboard(
  xterm: XTerm,
  element: HTMLElement,
  options: TerminalClipboardOptions = {},
): () => void {
  const { confirmPaste, onSelectionChange } = options
  const mac = isMac()
  // Async paste (confirm dialog / readText) can resolve after the terminal is
  // torn down by a reconnect or container switch, which disposes this xterm and
  // builds a new one. Pasting into the disposed instance silently misses the
  // visible session, so we gate the deferred paste on this flag.
  let disposed = false
  let selecting = false
  let moved = false
  let startX = 0
  let startY = 0
  let copyErrorReported = false
  const document = element.ownerDocument

  const selectionListener = xterm.onSelectionChange(() => {
    onSelectionChange?.(xterm.hasSelection())
  })

  const handleMouseDown = (event: MouseEvent) => {
    const forceSelection = mac
      ? event.altKey && xterm.options.macOptionClickForcesSelection
      : event.shiftKey
    const terminal = element.querySelector('.xterm')
    selecting = event.button === 0 && !(mac && event.ctrlKey) &&
      event.target instanceof Element && !!terminal?.contains(event.target) && !event.target.closest('.scrollbar') &&
      (xterm.modes.mouseTrackingMode === 'none' || !!forceSelection)
    moved = false
    startX = event.clientX
    startY = event.clientY
  }
  const handleMouseMove = (event: MouseEvent) => {
    if (!selecting) return
    if (!(event.buttons & 1)) selecting = false
    else if (event.clientX !== startX || event.clientY !== startY) moved = true
  }
  const cancelSelection = () => {
    selecting = false
  }
  const handleMouseUp = (event: MouseEvent) => {
    const primary = selecting && event.button === 0 &&
      (moved || event.detail >= 2 || event.shiftKey || (mac && event.altKey))
    selecting = false
    if (!primary || disposed) return
    const choice = options.copyOnSelect?.() ?? 'off'
    if (choice === 'pending' && xterm.hasSelection()) options.onCopyPending?.()
    if (choice !== 'on') return
    // xterm updates the selection model during mousedown and mousemove.
    // Read it on release, including when the same text is selected again.
    copyTerminalSelection(xterm, () => {
      if (disposed || copyErrorReported) return
      copyErrorReported = true
      options.onCopyError?.()
    }, () => {
      if (!disposed) copyErrorReported = false
    })
  }
  document.addEventListener('mousedown', handleMouseDown, true)
  document.addEventListener('mousemove', handleMouseMove, true)
  document.addEventListener('mouseup', handleMouseUp, true)
  document.defaultView?.addEventListener('blur', cancelSelection)

  const handlePaste = (e: ClipboardEvent) => {
    const text = e.clipboardData?.getData('text') ?? ''
    if (!text || !confirmPaste || !isRiskyMultilinePaste(xterm, text)) return
    e.preventDefault()
    e.stopImmediatePropagation()
    confirmPaste({ lineCount: pasteLineCount(text), text }).then((ok) => {
      if (ok && !disposed) xterm.paste(text)
    }).catch(() => {})
  }
  element.addEventListener('paste', handlePaste, true)

  let handleContextMenu: ((e: MouseEvent) => void) | undefined
  if (!mac) {
    handleContextMenu = (e: MouseEvent) => {
      if (!navigator.clipboard?.readText) return
      e.preventDefault()
      navigator.clipboard.readText().then(async (text) => {
        if (!text || disposed) return
        if (confirmPaste && isRiskyMultilinePaste(xterm, text)) {
          const ok = await confirmPaste({ lineCount: pasteLineCount(text), text })
          if (!ok || disposed) return
        }
        xterm.paste(text)
      }).catch(() => {})
    }
    element.addEventListener('contextmenu', handleContextMenu)
  }

  return () => {
    disposed = true
    selectionListener.dispose()
    document.removeEventListener('mousedown', handleMouseDown, true)
    document.removeEventListener('mousemove', handleMouseMove, true)
    document.removeEventListener('mouseup', handleMouseUp, true)
    document.defaultView?.removeEventListener('blur', cancelSelection)
    element.removeEventListener('paste', handlePaste, true)
    if (handleContextMenu) element.removeEventListener('contextmenu', handleContextMenu)
  }
}
