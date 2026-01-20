import { createContext, useContext, useState, useCallback, ReactNode } from 'react'
import { Check, Terminal, X } from 'lucide-react'
import { clsx } from 'clsx'

interface Toast {
  id: string
  message: string
  command?: string
  type?: 'success' | 'info' | 'warning'
  position?: { x: number; y: number }
}

interface ToastContextType {
  showToast: (message: string, options?: { command?: string; type?: Toast['type']; position?: { x: number; y: number } }) => void
  showCopied: (command: string, label?: string, event?: React.MouseEvent) => void
}

const ToastContext = createContext<ToastContextType | null>(null)

export function useToast() {
  const context = useContext(ToastContext)
  if (!context) {
    throw new Error('useToast must be used within ToastProvider')
  }
  return context
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])

  const showToast = useCallback((message: string, options?: { command?: string; type?: Toast['type']; position?: { x: number; y: number } }) => {
    const id = Math.random().toString(36).slice(2)
    const toast: Toast = { id, message, ...options }

    setToasts(prev => [...prev, toast])

    // Auto-dismiss after 5 seconds
    setTimeout(() => {
      setToasts(prev => prev.filter(t => t.id !== id))
    }, 5000)
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

  const dismissToast = useCallback((id: string) => {
    setToasts(prev => prev.filter(t => t.id !== id))
  }, [])

  return (
    <ToastContext.Provider value={{ showToast, showCopied }}>
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

  return (
    <div
      className={clsx(
        'flex items-start gap-3 p-3 rounded-lg shadow-xl border animate-in',
        'bg-slate-800 border-slate-700',
        'w-[400px] max-w-[calc(100vw-32px)]'
      )}
      style={style}
    >
      {/* Icon */}
      <div className={clsx(
        'w-8 h-8 rounded-full flex items-center justify-center shrink-0',
        toast.type === 'success' ? 'bg-green-500/20' : 'bg-indigo-500/20'
      )}>
        {toast.command ? (
          <Terminal className={clsx('w-4 h-4', toast.type === 'success' ? 'text-green-400' : 'text-indigo-400')} />
        ) : (
          <Check className="w-4 h-4 text-green-400" />
        )}
      </div>

      {/* Content */}
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-white">{toast.message}</span>
          <Check className="w-3.5 h-3.5 text-green-400 shrink-0" />
        </div>
        {toast.command && (
          <code className="block mt-1.5 text-xs text-slate-300 font-mono bg-slate-900 rounded px-2 py-1.5 whitespace-pre-wrap break-all">
            {toast.command}
          </code>
        )}
      </div>

      {/* Dismiss button */}
      <button
        onClick={onDismiss}
        className="p-1 text-slate-500 hover:text-white rounded hover:bg-slate-700 shrink-0"
      >
        <X className="w-4 h-4" />
      </button>
    </div>
  )
}
