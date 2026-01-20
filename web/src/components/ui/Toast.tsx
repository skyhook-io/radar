import { createContext, useContext, useState, useCallback, ReactNode } from 'react'
import { Check, Terminal, X } from 'lucide-react'
import { clsx } from 'clsx'

interface Toast {
  id: string
  message: string
  command?: string
  type?: 'success' | 'info' | 'warning'
}

interface ToastContextType {
  showToast: (message: string, options?: { command?: string; type?: Toast['type'] }) => void
  showCopied: (command: string, label?: string) => void
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

  const showToast = useCallback((message: string, options?: { command?: string; type?: Toast['type'] }) => {
    const id = Math.random().toString(36).slice(2)
    const toast: Toast = { id, message, ...options }

    setToasts(prev => [...prev, toast])

    // Auto-dismiss after 3 seconds
    setTimeout(() => {
      setToasts(prev => prev.filter(t => t.id !== id))
    }, 3000)
  }, [])

  const showCopied = useCallback((command: string, label?: string) => {
    navigator.clipboard.writeText(command)
    showToast(label || 'Copied to clipboard', { command, type: 'success' })
  }, [showToast])

  const dismissToast = useCallback((id: string) => {
    setToasts(prev => prev.filter(t => t.id !== id))
  }, [])

  return (
    <ToastContext.Provider value={{ showToast, showCopied }}>
      {children}

      {/* Toast container - bottom right */}
      <div className="fixed bottom-4 right-4 z-50 flex flex-col gap-2 max-w-md">
        {toasts.map(toast => (
          <ToastItem key={toast.id} toast={toast} onDismiss={() => dismissToast(toast.id)} />
        ))}
      </div>
    </ToastContext.Provider>
  )
}

function ToastItem({ toast, onDismiss }: { toast: Toast; onDismiss: () => void }) {
  return (
    <div
      className={clsx(
        'flex items-start gap-3 p-3 rounded-lg shadow-xl border animate-in',
        'bg-slate-800 border-slate-700'
      )}
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
          <code className="block mt-1.5 text-xs text-slate-400 font-mono bg-slate-900 rounded px-2 py-1.5 overflow-x-auto whitespace-nowrap">
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
