// Ctrl+= / Ctrl+- for the Windows desktop app. The View menu registers these
// accelerators, but Wails' Windows key table has no entry for "=" or "-", so
// they are silently dropped, and WebView2's own zoom keys are off. The page
// applies the same zoom the menu items do. Elsewhere the native accelerators
// work, so this only runs inside WebView2.
//
// Inside a terminal Ctrl+- is readline's undo, so the key is left to xterm
// there; View → Zoom still works.

const STEP = 0.1
const MIN = 0.5
const MAX = 2

export function zoomStep(e: Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'altKey' | 'metaKey'>): number {
  if (!e.ctrlKey || e.altKey || e.metaKey) return 0
  if (e.key === '=' || e.key === '+') return STEP
  if (e.key === '-' || e.key === '_') return -STEP
  return 0
}

export function installDesktopZoomShortcuts() {
  if (!(window as unknown as { chrome?: { webview?: unknown } }).chrome?.webview) return
  window.addEventListener('keydown', (e) => {
    const step = zoomStep(e)
    if (!step || (e.target as Element | null)?.closest?.('.xterm')) return
    e.preventDefault()
    const z = parseFloat(document.body.style.zoom || '1')
    document.body.style.zoom = String(Math.min(MAX, Math.max(MIN, Math.round((z + step) * 10) / 10)))
  }, true)
}
