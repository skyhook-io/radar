import { useRegisterSW } from 'virtual:pwa-register/react'
import { X } from 'lucide-react'

export function PWAUpdatePrompt() {
  const {
    needRefresh: [needRefresh, setNeedRefresh],
    updateServiceWorker,
  } = useRegisterSW()

  if (!needRefresh) return null

  return (
    <div className="fixed bottom-4 right-4 z-50 max-w-sm bg-theme-surface border border-accent/50 rounded-lg shadow-xl p-4 animate-in slide-in-from-right">
      <div className="flex items-start justify-between gap-2">
        <div>
          <p className="text-sm font-medium text-theme-text-primary">
            New version available
          </p>
          <p className="text-xs text-theme-text-secondary mt-1">
            Reload to get the latest updates.
          </p>
        </div>
        <button
          onClick={() => setNeedRefresh(false)}
          className="text-theme-text-tertiary hover:text-theme-text-primary transition-colors"
        >
          <X className="w-4 h-4" />
        </button>
      </div>
      <div className="flex gap-2 mt-3">
        <button
          onClick={() => updateServiceWorker(true)}
          className="px-3 py-1.5 text-xs font-medium bg-accent text-white rounded-md hover:opacity-90 transition-opacity"
        >
          Reload
        </button>
        <button
          onClick={() => setNeedRefresh(false)}
          className="px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary transition-colors"
        >
          Dismiss
        </button>
      </div>
    </div>
  )
}
