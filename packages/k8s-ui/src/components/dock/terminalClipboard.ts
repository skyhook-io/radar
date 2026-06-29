import type { Terminal as XTerm } from '@xterm/xterm'
import { isMac } from '../../utils/platform'

export interface PasteConfirmInfo {
  lineCount: number
  text: string
}

/** Asks the host to confirm a risky paste; resolves true to proceed. */
export type PasteConfirmer = (info: PasteConfirmInfo) => Promise<boolean>

export interface TerminalClipboardOptions {
  confirmPaste?: PasteConfirmer
  /** Notified when the selection changes; drives the toolbar Copy button's enabled state. */
  onSelectionChange?: (hasSelection: boolean) => void
}

/** Copies the terminal's current selection to the clipboard. No-op if nothing is selected. */
export function copyTerminalSelection(xterm: XTerm): void {
  const selection = xterm.getSelection()
  if (selection) navigator.clipboard.writeText(selection).catch(() => {})
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
 * Copy is explicit — the host's toolbar Copy button (via copyTerminalSelection)
 * and ⌘C on macOS (Ctrl+C stays SIGINT). We deliberately do NOT copy on
 * selection: a browser terminal has a single system clipboard with no separate
 * PRIMARY buffer, so copy-on-select would clobber the clipboard on every
 * incidental drag. `onSelectionChange` just reports selection presence so the
 * host can enable/disable its Copy button.
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

  const selectionListener = xterm.onSelectionChange(() => {
    onSelectionChange?.(xterm.hasSelection())
  })

  // macOS: ⌘C copies the selection. Ctrl+C is left untouched so it still sends
  // SIGINT. Other platforms have no conflict-free copy keystroke in a browser
  // (Ctrl+C = SIGINT, Ctrl+Shift+C = devtools), so they rely on the Copy button.
  if (mac) {
    xterm.attachCustomKeyEventHandler((e) => {
      if (
        e.type === 'keydown' && e.metaKey && !e.ctrlKey && !e.altKey && !e.shiftKey &&
        (e.key === 'c' || e.key === 'C') && xterm.hasSelection()
      ) {
        e.preventDefault()
        copyTerminalSelection(xterm)
        return false
      }
      return true
    })
  }

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
    element.removeEventListener('paste', handlePaste, true)
    if (handleContextMenu) element.removeEventListener('contextmenu', handleContextMenu)
  }
}
