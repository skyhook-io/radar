import type { ReactNode } from 'react'
import { openCloudFunnel, type CloudHintEntry } from './cloudHints'

// The text link every in-context hint uses: Radar's standard inline link
// (accent text), so it reads as clickable inside a muted sentence. Opens the
// one Cloud dialog. Callers gate on useCloudHintsEnabled.
export function CloudHintLink({
  entry,
  children = 'Radar Cloud',
  onBeforeOpen,
}: {
  entry: CloudHintEntry
  children?: ReactNode
  // Lets a host close its own dialog first (through its close guard) and open
  // the Cloud dialog only once that has happened.
  onBeforeOpen?: (open: () => void) => void
}) {
  const open = () => openCloudFunnel(entry)
  return (
    <button
      type="button"
      onClick={() => (onBeforeOpen ? onBeforeOpen(open) : open())}
      className="font-medium text-accent-text underline-offset-2 hover:underline"
    >
      {children}
    </button>
  )
}
