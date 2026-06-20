import { useCallback, useRef, useState } from 'react'
import { ConfirmDialog } from '../ui/ConfirmDialog'
import type { PasteConfirmInfo, PasteConfirmer } from './terminalClipboard'

// Session-scoped "Don't ask again" — app-wide for the page session (shared across
// terminal tabs), reset on reload. Deliberately not persisted: a multi-line paste
// warning re-appearing once per session is cheap, and it avoids a safety net the
// user silently disabled long ago and forgot.
let warningSuppressedThisSession = false

/**
 * Owns the themed confirmation shown before a risky multi-line paste runs in a
 * terminal. Returns a `confirmPaste` callback to hand to setupTerminalClipboard
 * and a `pasteDialog` node the host must render. Once the user opts out via
 * "Don't ask again", pastes proceed without prompting for the rest of the session.
 */
export function useMultilinePasteConfirm(): { confirmPaste: PasteConfirmer; pasteDialog: React.ReactNode } {
  const [pending, setPending] = useState<PasteConfirmInfo | null>(null)
  const [dontAskAgain, setDontAskAgain] = useState(false)
  const resolverRef = useRef<((ok: boolean) => void) | null>(null)

  const confirmPaste = useCallback<PasteConfirmer>(
    (info) => {
      if (warningSuppressedThisSession) return Promise.resolve(true)
      // A paste arriving while a dialog is still open supersedes it: decline the
      // pending one so its promise resolves (the superseded paste is dropped)
      // rather than leaking, then show the new prompt.
      resolverRef.current?.(false)
      return new Promise<boolean>((resolve) => {
        resolverRef.current = resolve
        setDontAskAgain(false)
        setPending(info)
      })
    },
    [],
  )

  const settle = useCallback(
    (ok: boolean) => {
      // Only suppress when the user actually proceeds — cancelling shouldn't
      // quietly disable the warning.
      if (ok && dontAskAgain) warningSuppressedThisSession = true
      resolverRef.current?.(ok)
      resolverRef.current = null
      setPending(null)
    },
    [dontAskAgain],
  )

  const pasteDialog = pending ? (
    <ConfirmDialog
      open
      variant="warning"
      title="Paste multiple lines?"
      message={`This pastes ${pending.lineCount} lines into the shell — they run immediately.`}
      details={pending.text}
      confirmLabel="Paste"
      cancelLabel="Cancel"
      onConfirm={() => settle(true)}
      onClose={() => settle(false)}
    >
      <label className="flex items-center gap-2 text-sm text-theme-text-secondary cursor-pointer">
        <input
          type="checkbox"
          checked={dontAskAgain}
          onChange={(e) => setDontAskAgain(e.target.checked)}
          className="w-4 h-4 rounded border-theme-border bg-theme-base accent-amber-500"
        />
        <span>Don&apos;t ask again this session</span>
      </label>
    </ConfirmDialog>
  ) : null

  return { confirmPaste, pasteDialog }
}
