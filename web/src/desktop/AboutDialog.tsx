// Help → About Radar in the Windows desktop app. macOS keeps its native About
// panel; the desktop app opens this one by dispatching 'radar:open-about' with
// the exact build version and platform (cmd/desktop/menu.go).
import { useId, useState } from 'react'
import { Check, Copy, X } from 'lucide-react'
import { DialogPortal, assetUrl } from '@skyhook-io/k8s-ui'
import { useVersionCheck } from '../api/client'
import { openExternal } from '../utils/navigation'
import { hasDesktopMenu, runDesktopMenuItem } from './WindowsTitleBar'

export type AboutInfo = { version: string; platform: string }

const LINKS = [
  { label: 'Website', url: 'https://radarhq.io' },
  { label: 'Documentation', url: 'https://radarhq.io/docs' },
  { label: 'GitHub', url: 'https://github.com/skyhook-io/radar' },
]

const CHANNEL_LABEL: Record<string, string> = {
  prerelease: 'pre-release',
  development: 'development build',
  custom: 'custom build',
}

export function aboutVersionText(info: AboutInfo, channel?: string): string {
  const label = channel ? CHANNEL_LABEL[channel] : undefined
  return `Radar ${info.version.replace(/^v/, '')}${label ? ` (${label})` : ''}\n${info.platform} · desktop app`
}

export function AboutDialog({
  info,
  onClose,
  onShowWhatsNew,
  onReportIssue,
}: {
  info: AboutInfo | null
  onClose: () => void
  onShowWhatsNew?: () => void
  onReportIssue: () => void
}) {
  const titleId = useId()
  const { data: versionInfo } = useVersionCheck()
  const [copied, setCopied] = useState(false)
  // Keep the last info through the exit animation.
  const [shown, setShown] = useState<AboutInfo | null>(info)
  if (info && info !== shown) setShown(info)
  const current = info ?? shown

  const channel = versionInfo?.buildChannel
  const version = current?.version.replace(/^v/, '') ?? ''
  const status = versionInfo?.updateAvailable
    ? `Radar ${versionInfo.latestVersion?.replace(/^v/, '')} is available`
    : versionInfo?.latestVersion && channel === 'stable'
      ? "You're up to date"
      : null

  const copyVersion = async () => {
    if (!current) return
    try {
      await navigator.clipboard.writeText(aboutVersionText(current, channel))
      setCopied(true)
      window.setTimeout(() => setCopied(false), 2000)
    } catch {
      console.warn('Failed to copy version info')
    }
  }

  const then = (action: () => void) => () => {
    onClose()
    action()
  }

  return (
    <DialogPortal open={!!info} onClose={onClose} className="w-full max-w-md" ariaLabelledBy={titleId}>
      <div className="flex items-start gap-3 p-5 pb-4">
        <span className="w-11 h-11 shrink-0 rounded-xl overflow-hidden bg-emerald-500/10 border border-emerald-500/20">
          <img src={assetUrl('/images/radar/radar-icon.svg')} alt="" aria-hidden className="w-full h-full p-1" />
        </span>
        <div className="flex-1 min-w-0">
          <h2 id={titleId} className="text-lg font-semibold tracking-tight text-theme-text-primary">Radar</h2>
          <p className="text-sm text-theme-text-secondary">The open-source Kubernetes UI</p>
          <p className="text-xs text-theme-text-tertiary">by Skyhook</p>
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close"
          className="p-1 -m-1 rounded text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover"
        >
          <X className="w-4 h-4" />
        </button>
      </div>

      <dl className="mx-5 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
        <dt className="text-theme-text-tertiary">Version</dt>
        <dd className="text-theme-text-primary">
          {version}
          {channel && CHANNEL_LABEL[channel] && <span className="text-theme-text-tertiary"> · {CHANNEL_LABEL[channel]}</span>}
          <button
            type="button"
            onClick={copyVersion}
            aria-label="Copy version info"
            title="Copy version info"
            className="ml-2 inline-flex items-center gap-1 align-middle text-xs text-theme-text-tertiary hover:text-theme-text-primary"
          >
            {copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
            {copied ? 'Copied' : 'Copy'}
          </button>
          {status && <div className="text-xs text-theme-text-secondary mt-0.5">{status}</div>}
        </dd>
        <dt className="text-theme-text-tertiary">Platform</dt>
        <dd className="text-theme-text-primary">{current?.platform}</dd>
        <dt className="text-theme-text-tertiary">License</dt>
        <dd className="text-theme-text-primary">Apache 2.0</dd>
      </dl>

      {(hasDesktopMenu() || onShowWhatsNew) && (
        <div className="mx-5 mt-4 flex flex-wrap gap-2">
          {hasDesktopMenu() && (
            <button type="button" className="btn-brand text-sm px-3 py-1.5" onClick={then(() => runDesktopMenuItem('Help/Check for Updates...'))}>
              Check for updates
            </button>
          )}
          {onShowWhatsNew && (
            <button
              type="button"
              className="text-sm px-3 py-1.5 rounded-md border border-theme-border text-theme-text-primary hover:bg-theme-hover"
              onClick={then(onShowWhatsNew)}
            >
              What's new
            </button>
          )}
        </div>
      )}

      <div className="mt-5 px-5 py-3 border-t border-theme-border text-sm">
        <div className="flex flex-wrap gap-x-4 gap-y-1">
          {LINKS.map((l) => (
            <button key={l.url} type="button" className="text-accent hover:underline" onClick={() => openExternal(l.url)}>
              {l.label}
            </button>
          ))}
          <button type="button" className="text-accent hover:underline" onClick={then(onReportIssue)}>
            Report an issue
          </button>
        </div>
      </div>
    </DialogPortal>
  )
}
