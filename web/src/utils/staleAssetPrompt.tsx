import { RotateCw } from 'lucide-react'
import { showApiNotice } from '../components/ui/Toast'

export const STALE_ASSET_TOAST_ID = 'stale-asset'

// A tab opened before Radar was upgraded asks for chunks under hashes the new
// build no longer serves, and Chromium keeps a failed dynamic import failed
// until the page reloads, so a reload is the only thing that brings the
// feature back. The event is not prevented: the feature's own fallback (the
// YAML editor's plain textarea, say) still runs, and the page never reloads
// by itself because the user may have unsaved edits. Views retry their chunk
// on every refresh, so the prompt is shown once and stays dismissed.
export function installStaleAssetPrompt(target: Window = window, reload: () => void = () => window.location.reload()): () => void {
  let shown = false
  const onPreloadError = (event: VitePreloadErrorEvent) => {
    console.error('[Radar] Failed to load part of the app:', event.payload)
    if (shown) return
    shown = true
    showApiNotice(
      'Part of Radar failed to load',
      'Radar may have been updated since this page was opened. Reload to get the current version; unsaved edits on this page will be lost.',
      'warning',
      {
        id: STALE_ASSET_TOAST_ID,
        duration: 0,
        action: { label: 'Reload', icon: <RotateCw className="w-3.5 h-3.5" />, onClick: reload },
      },
    )
  }
  target.addEventListener('vite:preloadError', onPreloadError)
  return () => target.removeEventListener('vite:preloadError', onPreloadError)
}
