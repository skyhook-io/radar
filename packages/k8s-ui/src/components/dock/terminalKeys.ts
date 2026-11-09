import type { Terminal as XTerm } from '@xterm/xterm'
import { isMac } from '../../utils/platform'
import { copyTerminalSelection } from './terminalClipboard'

type KeyLike = Pick<KeyboardEvent, 'type' | 'key' | 'metaKey' | 'ctrlKey' | 'altKey' | 'shiftKey'>

/**
 * The terminal's find shortcut: ⌘F on macOS, Ctrl+Shift+F elsewhere. Plain
 * Ctrl+F is left to the shell (readline forward-char, page-forward in less and
 * vim). Shift turns the key into "F", so the comparison is case-insensitive.
 */
export function isTerminalFindShortcut(e: KeyLike, mac: boolean = isMac()): boolean {
  if (e.type !== 'keydown' || e.key.toLowerCase() !== 'f' || e.altKey) return false
  return mac
    ? e.metaKey && !e.ctrlKey && !e.shiftKey
    : e.ctrlKey && e.shiftKey && !e.metaKey
}

export interface TerminalKeyOptions {
  /** Called on the find shortcut while the terminal has focus. */
  onFind: () => void
}

/**
 * Installs the terminal's one custom key handler. xterm keeps a single handler
 * (a second attachCustomKeyEventHandler call replaces the first), so every key
 * the terminal intercepts lives here.
 *
 * A handler returning false makes xterm skip the key but does not stop the
 * event, so intercepted keys stop propagation themselves: otherwise ⌘F would
 * also reach window listeners such as a logs tab mounted elsewhere in the dock.
 */
export function attachTerminalKeys(xterm: XTerm, { onFind }: TerminalKeyOptions): void {
  const mac = isMac()
  xterm.attachCustomKeyEventHandler((e) => {
    if (isTerminalFindShortcut(e, mac)) {
      e.preventDefault()
      e.stopPropagation()
      onFind()
      return false
    }
    // macOS: ⌘C copies the selection. Ctrl+C is left untouched so it still sends
    // SIGINT. Other platforms have no conflict-free copy keystroke in a browser
    // (Ctrl+C = SIGINT, Ctrl+Shift+C = devtools), so they rely on the Copy button.
    if (
      mac && e.type === 'keydown' && e.metaKey && !e.ctrlKey && !e.altKey && !e.shiftKey &&
      (e.key === 'c' || e.key === 'C') && xterm.hasSelection()
    ) {
      e.preventDefault()
      copyTerminalSelection(xterm)
      return false
    }
    return true
  })
}
