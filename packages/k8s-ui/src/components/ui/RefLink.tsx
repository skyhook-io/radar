import type { ReactNode } from 'react'
import { clsx } from 'clsx'
import type { ResourceRef } from '../../types/core'

export type NavigateToRef = (ref: ResourceRef) => void

/** A reference to another object: a link when the host can navigate, plain text otherwise. */
export function RefLink({ refTo, onNavigate, children, mono }: { refTo: ResourceRef; onNavigate?: NavigateToRef; children?: ReactNode; mono?: boolean }) {
  const label = children ?? refTo.name
  if (!onNavigate) return <span className={clsx(mono && 'font-mono')}>{label}</span>
  return (
    <button
      type="button"
      onClick={() => onNavigate(refTo)}
      className={clsx('text-accent-text hover:underline text-left break-all', mono && 'font-mono')}
    >
      {label}
    </button>
  )
}
