import type { ReactNode } from 'react'
import { Copy, Check, Minus } from 'lucide-react'
import { isMac } from '../../utils/platform'
import { RowActionMenu } from '../ui/RowActionMenu'
import { useTerminalCopyOnSelect } from './terminalPreferences'

/**
 * Right-aligned clipboard affordances for a terminal mini-toolbar: an explicit
 * Copy button (enabled only when there's a selection), terminal preferences,
 * and a muted paste hint that truncates on narrow docks. Shared by TerminalTab
 * and LocalTerminalTab. `leading` renders just before Copy.
 */
export function TerminalClipboardToolbar({
  hasSelection,
  onCopy,
  leading,
}: {
  hasSelection: boolean
  onCopy: () => void
  leading?: ReactNode
}) {
  const copyOnSelect = useTerminalCopyOnSelect()
  const clipboardAvailable = typeof navigator.clipboard?.writeText === 'function'
  // Copy is an explicit button (+ ⌘C on macOS), so the hint just covers paste.
  const hint = isMac() ? '⌘V to paste' : 'Right-click or Ctrl+V to paste'
  return (
    <>
      <div className="ml-auto flex shrink-0 items-center gap-2">
        {leading}
        <button
          onClick={onCopy}
          disabled={!hasSelection || !clipboardAvailable}
          title={clipboardAvailable ? 'Copy selection' : 'Clipboard access requires HTTPS or localhost'}
          className="flex items-center gap-1 px-2 py-0.5 text-xs text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded disabled:opacity-40 disabled:hover:bg-transparent disabled:cursor-default"
        >
          <Copy className="w-3 h-3" />
          Copy
        </button>
        {copyOnSelect.hasProvider && <RowActionMenu
          ariaLabel="Terminal options"
          items={[{
            key: 'copy-on-select',
            label: 'Copy on select',
            checked: copyOnSelect.enabled,
            disabled: !copyOnSelect.available || !clipboardAvailable,
            disabledReason: !copyOnSelect.available
              ? copyOnSelect.unavailableReason
              : !clipboardAvailable ? 'Clipboard access requires HTTPS or localhost' : undefined,
            icon: copyOnSelect.enabled ? Check : Minus,
            onClick: () => copyOnSelect.setEnabled(!copyOnSelect.enabled),
          }]}
        />}
      </div>
      <span className="min-w-0 truncate text-[11px] text-theme-text-tertiary/70" title={hint}>
        {hint}
      </span>
    </>
  )
}
