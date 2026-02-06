import { useState, useEffect } from 'react'
import { Download, X } from 'lucide-react'
import { useVersionCheck } from '../../api/client'

const DISMISSED_KEY = 'radar-update-dismissed'

export function UpdateNotification() {
  const { data: versionInfo } = useVersionCheck()
  const [dismissed, setDismissed] = useState(false)

  // Check if this version was already dismissed
  useEffect(() => {
    if (versionInfo?.latestVersion) {
      const dismissedVersion = localStorage.getItem(DISMISSED_KEY)
      if (dismissedVersion === versionInfo.latestVersion) {
        setDismissed(true)
      }
    }
  }, [versionInfo?.latestVersion])

  const handleDismiss = () => {
    if (versionInfo?.latestVersion) {
      localStorage.setItem(DISMISSED_KEY, versionInfo.latestVersion)
    }
    setDismissed(true)
  }

  // Don't show if no update available, dismissed, or error
  if (!versionInfo?.updateAvailable || dismissed) {
    return null
  }

  return (
    <div className="fixed bottom-4 right-4 z-50 max-w-sm bg-theme-surface border border-blue-500/50 rounded-lg shadow-xl p-4 animate-in slide-in-from-right">
      <div className="flex items-start gap-3">
        <div className="flex items-center justify-center w-8 h-8 bg-blue-500/20 rounded-full shrink-0">
          <Download className="w-4 h-4 text-blue-400" />
        </div>
        <div className="flex-1 min-w-0">
          <h4 className="text-sm font-medium text-theme-text-primary">
            Update Available
          </h4>
          <p className="text-xs text-theme-text-secondary mt-1">
            Radar {versionInfo.latestVersion} is available.{' '}
            You're on {versionInfo.currentVersion}.
          </p>
          {versionInfo.releaseUrl && (
            <a
              href={versionInfo.releaseUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 mt-2 text-xs font-medium text-blue-400 hover:text-blue-300"
            >
              View release notes →
            </a>
          )}
        </div>
        <button
          onClick={handleDismiss}
          className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded shrink-0"
          aria-label="Dismiss"
        >
          <X className="w-4 h-4" />
        </button>
      </div>
    </div>
  )
}
