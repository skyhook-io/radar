import { useState, useEffect } from 'react'
import { Download, X, Copy, Check } from 'lucide-react'
import { useVersionCheck } from '../../api/client'

const DISMISSED_KEY = 'radar-update-dismissed'

export function UpdateNotification() {
  const { data: versionInfo } = useVersionCheck()
  const [dismissed, setDismissed] = useState(false)
  const [copied, setCopied] = useState(false)
  const [copyFailed, setCopyFailed] = useState(false)

  // Log version check errors for debugging
  useEffect(() => {
    if (versionInfo?.error) {
      console.debug('[radar] Version check failed:', versionInfo.error)
    }
  }, [versionInfo?.error])

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

  const handleCopyCommand = async () => {
    if (versionInfo?.updateCommand) {
      try {
        await navigator.clipboard.writeText(versionInfo.updateCommand)
        setCopied(true)
        setTimeout(() => setCopied(false), 2000)
      } catch (err) {
        console.debug('[radar] Clipboard write failed:', err)
        setCopyFailed(true)
        setTimeout(() => setCopyFailed(false), 2000)
      }
    }
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

          {/* Show update command with copy button for package managers */}
          {versionInfo.updateCommand ? (
            <button
              onClick={handleCopyCommand}
              className="flex items-center gap-2 mt-2 px-2 py-1.5 bg-theme-elevated rounded text-xs font-mono text-theme-text-primary hover:bg-theme-surface-hover transition-colors w-full"
            >
              <code className="flex-1 text-left truncate">{versionInfo.updateCommand}</code>
              {copied ? (
                <Check className="w-3.5 h-3.5 text-green-400 shrink-0" />
              ) : copyFailed ? (
                <X className="w-3.5 h-3.5 text-red-400 shrink-0" />
              ) : (
                <Copy className="w-3.5 h-3.5 text-theme-text-tertiary shrink-0" />
              )}
            </button>
          ) : (
            /* Direct download - show release link */
            versionInfo.releaseUrl && (
              <a
                href={versionInfo.releaseUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 mt-2 text-xs font-medium text-blue-400 hover:text-blue-300"
              >
                Download from GitHub →
              </a>
            )
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
