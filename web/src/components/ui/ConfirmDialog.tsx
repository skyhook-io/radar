import { useEffect, useRef } from 'react'
import { AlertTriangle, X } from 'lucide-react'
import { clsx } from 'clsx'

interface ConfirmDialogProps {
  open: boolean
  onClose: () => void
  onConfirm: () => void
  title: string
  message: string
  details?: string
  confirmLabel?: string
  cancelLabel?: string
  variant?: 'danger' | 'warning'
  isLoading?: boolean
}

export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  message,
  details,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  variant = 'danger',
  isLoading = false,
}: ConfirmDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null)

  // Handle ESC key
  useEffect(() => {
    if (!open) return

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !isLoading) {
        onClose()
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [open, onClose, isLoading])

  // Focus trap
  useEffect(() => {
    if (open && dialogRef.current) {
      dialogRef.current.focus()
    }
  }, [open])

  if (!open) return null

  const isDanger = variant === 'danger'

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-black/60 backdrop-blur-sm"
        onClick={isLoading ? undefined : onClose}
      />

      {/* Dialog */}
      <div
        ref={dialogRef}
        tabIndex={-1}
        className="relative bg-theme-surface border border-theme-border rounded-lg shadow-2xl max-w-md w-full mx-4 outline-none"
      >
        {/* Header */}
        <div className="flex items-start gap-3 p-4 border-b border-theme-border">
          <div
            className={clsx(
              'flex items-center justify-center w-10 h-10 rounded-full shrink-0',
              isDanger ? 'bg-red-500/20' : 'bg-amber-500/20'
            )}
          >
            <AlertTriangle
              className={clsx('w-5 h-5', isDanger ? 'text-red-400' : 'text-amber-400')}
            />
          </div>
          <div className="flex-1 min-w-0">
            <h3 className="text-lg font-semibold text-theme-text-primary">{title}</h3>
            <p className="text-sm text-theme-text-secondary mt-1">{message}</p>
          </div>
          <button
            onClick={onClose}
            disabled={isLoading}
            className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded disabled:opacity-50"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Details */}
        {details && (
          <div className="p-4 border-b border-theme-border">
            <pre className="text-xs text-theme-text-secondary bg-theme-base/50 rounded p-3 overflow-auto max-h-32 whitespace-pre-wrap font-mono">
              {details}
            </pre>
          </div>
        )}

        {/* Warning message */}
        <div className="p-4">
          <div
            className={clsx(
              'flex items-start gap-2 p-3 rounded text-sm',
              isDanger
                ? 'bg-red-500/10 border border-red-500/30 text-red-300'
                : 'bg-amber-500/10 border border-amber-500/30 text-amber-300'
            )}
          >
            <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
            <span>
              {isDanger
                ? 'This action cannot be undone. Please make sure you want to proceed.'
                : 'Please confirm you want to proceed with this action.'}
            </span>
          </div>
        </div>

        {/* Actions */}
        <div className="flex items-center justify-end gap-3 p-4 border-t border-theme-border">
          <button
            onClick={onClose}
            disabled={isLoading}
            className="px-4 py-2 text-sm font-medium text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg transition-colors disabled:opacity-50"
          >
            {cancelLabel}
          </button>
          <button
            onClick={onConfirm}
            disabled={isLoading}
            className={clsx(
              'px-4 py-2 text-sm font-medium rounded-lg transition-colors disabled:opacity-50 flex items-center gap-2',
              isDanger
                ? 'bg-red-600 hover:bg-red-700 text-theme-text-primary'
                : 'bg-amber-600 hover:bg-amber-700 text-theme-text-primary'
            )}
          >
            {isLoading && (
              <svg className="animate-spin h-4 w-4" viewBox="0 0 24 24">
                <circle
                  className="opacity-25"
                  cx="12"
                  cy="12"
                  r="10"
                  stroke="currentColor"
                  strokeWidth="4"
                  fill="none"
                />
                <path
                  className="opacity-75"
                  fill="currentColor"
                  d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                />
              </svg>
            )}
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
