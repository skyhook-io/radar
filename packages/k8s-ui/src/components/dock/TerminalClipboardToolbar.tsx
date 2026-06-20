import { Copy } from 'lucide-react'
import { isMac } from '../../utils/platform'

/**
 * Right-aligned clipboard affordances for a terminal mini-toolbar: an explicit
 * Copy button (enabled only when there's a selection — we don't copy-on-select)
 * and a muted paste hint that truncates on narrow docks. Shared by TerminalTab
 * and LocalTerminalTab.
 */
export function TerminalClipboardToolbar({
  hasSelection,
  onCopy,
}: {
  hasSelection: boolean
  onCopy: () => void
}) {
  // Copy is an explicit button (+ ⌘C on macOS), so the hint just covers paste.
  const hint = isMac() ? '⌘V to paste' : 'Right-click or Ctrl+V to paste'
  return (
    <>
      <button
        onClick={onCopy}
        disabled={!hasSelection}
        title="Copy selection"
        className="ml-auto flex items-center gap-1 px-2 py-0.5 text-xs text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded disabled:opacity-40 disabled:hover:bg-transparent disabled:cursor-default"
      >
        <Copy className="w-3 h-3" />
        Copy
      </button>
      <span className="min-w-0 truncate text-[11px] text-theme-text-tertiary/70" title={hint}>
        {hint}
      </span>
    </>
  )
}
