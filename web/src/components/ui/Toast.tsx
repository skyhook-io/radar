import { createContext, useContext, useState, useCallback, ReactNode, useEffect } from 'react'
import { Check, Terminal, X, AlertTriangle } from 'lucide-react'
import { clsx } from 'clsx'

interface Toast {
  id: string
  message: string
  detail?: string
  command?: string
  type?: 'success' | 'info' | 'warning' | 'error'
  position?: { x: number; y: number }
}

interface ToastContextType {
  showToast: (message: string, options?: { detail?: string; command?: string; type?: Toast['type']; position?: { x: number; y: number } }) => void
  showCopied: (command: string, label?: string, event?: React.MouseEvent) => void
  showError: (message: string, detail?: string) => void
  showSuccess: (message: string, detail?: string) => void
}

const ToastContext = createContext<ToastContextType | null>(null)

// Singleton pattern for showing toasts outside React components (e.g., in API error handlers)
class ToastManager {
  private static instance: ToastManager
  private showErrorFn: ((message: string, detail?: string) => void) | null = null
  private showSuccessFn: ((message: string, detail?: string) => void) | null = null

  static getInstance(): ToastManager {
    if (!ToastManager.instance) {
      ToastManager.instance = new ToastManager()
    }
    return ToastManager.instance
  }

  register(showError: typeof this.showErrorFn, showSuccess: typeof this.showSuccessFn) {
    this.showErrorFn = showError
    this.showSuccessFn = showSuccess
  }

  unregister() {
    this.showErrorFn = null
    this.showSuccessFn = null
  }

  showError(message: string, detail?: string) {
    this.showErrorFn ? this.showErrorFn(message, detail) : console.error('[Toast]', message, detail)
  }

  showSuccess(message: string, detail?: string) {
    this.showSuccessFn ? this.showSuccessFn(message, detail) : console.log('[Toast]', message, detail)
  }
}

const toastManager = ToastManager.getInstance()

export function showApiError(message: string, detail?: string) {
  toastManager.showError(message, detail)
}

export function showApiSuccess(message: string, detail?: string) {
  toastManager.showSuccess(message, detail)
}

export function useToast() {
  const context = useContext(ToastContext)
  if (!context) {
    throw new Error('useToast must be used within ToastProvider')
  }
  return context
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])

  const showToast = useCallback((message: string, options?: { detail?: string; command?: string; type?: Toast['type']; position?: { x: number; y: number } }) => {
    const id = Math.random().toString(36).slice(2)
    const toast: Toast = { id, message, ...options }

    setToasts(prev => [...prev, toast])

    // Auto-dismiss: errors stay longer (8s), others 5s
    const dismissTime = options?.type === 'error' ? 8000 : 5000
    setTimeout(() => {
      setToasts(prev => prev.filter(t => t.id !== id))
    }, dismissTime)
  }, [])

  const showCopied = useCallback((command: string, label?: string, event?: React.MouseEvent) => {
    navigator.clipboard.writeText(command)

    // Get position from click event
    let position: { x: number; y: number } | undefined
    if (event) {
      const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
      position = { x: rect.left, y: rect.bottom + 8 }
    }

    showToast(label || 'Copied to clipboard', { command, type: 'success', position })
  }, [showToast])

  const showError = useCallback((message: string, detail?: string) => {
    showToast(message, { detail, type: 'error' })
  }, [showToast])

  const showSuccess = useCallback((message: string, detail?: string) => {
    showToast(message, { detail, type: 'success' })
  }, [showToast])

  const dismissToast = useCallback((id: string) => {
    setToasts(prev => prev.filter(t => t.id !== id))
  }, [])

  // Wire up singleton for use outside React components
  useEffect(() => {
    toastManager.register(showError, showSuccess)
    return () => toastManager.unregister()
  }, [showError, showSuccess])

  return (
    <ToastContext.Provider value={{ showToast, showCopied, showError, showSuccess }}>
      {children}

      {/* Render toasts */}
      {toasts.map(toast => (
        <ToastItem key={toast.id} toast={toast} onDismiss={() => dismissToast(toast.id)} />
      ))}
    </ToastContext.Provider>
  )
}

function ToastItem({ toast, onDismiss }: { toast: Toast; onDismiss: () => void }) {
  // Calculate position - either near button or default to bottom-right
  const style: React.CSSProperties = toast.position
    ? {
        position: 'fixed',
        left: Math.min(toast.position.x, window.innerWidth - 420),
        top: toast.position.y,
        zIndex: 50,
      }
    : {
        position: 'fixed',
        bottom: 16,
        right: 16,
        zIndex: 50,
      }

  const isError = toast.type === 'error'
  const isSuccess = toast.type === 'success'

  return (
    <div
      className={clsx(
        'flex items-start gap-3 p-3 rounded-lg shadow-2xl border animate-in',
        'w-[400px] max-w-[calc(100vw-32px)]',
        isError
          ? 'bg-red-950/90 border-red-800/50'
          : isSuccess
            ? 'bg-emerald-950/90 border-emerald-700/50 dark:bg-emerald-950/90 dark:border-emerald-700/50'
            : 'bg-theme-surface border-theme-border'
      )}
      style={style}
    >
      {/* Icon */}
      <div className={clsx(
        'w-8 h-8 rounded-full flex items-center justify-center shrink-0',
        isError ? 'bg-red-500/20' :
        isSuccess ? 'bg-green-500/20' : 'bg-blue-500/20'
      )}>
        {isError ? (
          <AlertTriangle className="w-4 h-4 text-red-400" />
        ) : toast.command ? (
          <Terminal className={clsx('w-4 h-4', isSuccess ? 'text-green-400' : 'text-blue-400')} />
        ) : (
          <Check className="w-4 h-4 text-green-400" />
        )}
      </div>

      {/* Content */}
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className={clsx('text-sm font-medium', isError ? 'text-red-200' : isSuccess ? 'text-emerald-100' : 'text-theme-text-primary')}>
            {toast.message}
          </span>
          {!isError && <Check className={clsx('w-3.5 h-3.5 shrink-0', isSuccess ? 'text-emerald-300' : 'text-green-400')} />}
        </div>
        {toast.detail && (
          <p className={clsx('mt-1 text-xs', isError ? 'text-red-300/80' : isSuccess ? 'text-emerald-300/80' : 'text-theme-text-secondary')}>
            {toast.detail}
          </p>
        )}
        {toast.command && (
          <code className="block mt-1.5 text-xs text-theme-text-secondary font-mono bg-theme-base rounded px-2 py-1.5 whitespace-pre-wrap break-all">
            {toast.command}
          </code>
        )}
      </div>

      {/* Dismiss button */}
      <button
        onClick={onDismiss}
        className={clsx(
          'p-1 rounded shrink-0',
          isError
            ? 'text-red-400 hover:text-red-300 hover:bg-red-900/50'
            : isSuccess
              ? 'text-emerald-400 hover:text-emerald-300 hover:bg-emerald-900/50'
              : 'text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated'
        )}
      >
        <X className="w-4 h-4" />
      </button>
    </div>
  )
}
