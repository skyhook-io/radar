import React from 'react'
import ReactDOM from 'react-dom/client'
import { RadarApp } from './RadarApp'
import { openExternal } from './utils/navigation'
import { installWailsClipboardShim } from './utils/wails-clipboard'
import './index.css'

// Intercept external link clicks in the Wails desktop app.
// <a target="_blank"> is swallowed by WKWebView/WebView2 — route through openExternal()
// which calls the backend /api/desktop/open-url endpoint to open in the system browser.
window.addEventListener('click', (e: MouseEvent) => {
  const anchor = (e.target as HTMLElement).closest?.('a[href]') as HTMLAnchorElement | null
  if (!anchor) return
  const href = anchor.href
  if (!href || href.startsWith(window.location.origin) || href.startsWith('/') || href.startsWith('#') || href.startsWith('blob:')) return
  // External URL — open via system browser
  e.preventDefault()
  openExternal(href)
})

// Wails desktop clipboard shim — see web/src/utils/wails-clipboard.ts for the
// full WKWebView background and what each interception exists for.
installWailsClipboardShim()

// Mouse back/forward button navigation (button 3 = back, button 4 = forward).
// Uses 'mouseup' in capture phase to intercept before the browser's native handler.
// This prevents double-navigation in browsers (where auxclick + native both fire)
// and handles desktop WebView (Windows/Linux) where native handling varies.
// On macOS WKWebView, mouse events don't reach JS — native NSEvent monitor in
// mouse_darwin.go handles them via WKWebView.goBack()/goForward() directly.
window.addEventListener('mouseup', (e: MouseEvent) => {
  if (e.button === 3) {
    e.preventDefault()
    window.history.back()
  } else if (e.button === 4) {
    e.preventDefault()
    window.history.forward()
  }
}, true)


// Standalone Radar binary: same-origin API, router at root. It owns the whole
// tab, so it opts into per-view document.title. Library consumers (e.g.
// radar-hub-web) render <RadarApp apiBase="..." basename="..." /> WITHOUT this
// flag, keeping their own tab title.
const runtimeConfig = window.__RADAR_RUNTIME_CONFIG__

ReactDOM.createRoot(document.getElementById('radar')!).render(
  <React.StrictMode>
    <RadarApp
      apiBase={runtimeConfig?.apiBase}
      basename={runtimeConfig?.basePath}
      manageDocumentTitle
    />
  </React.StrictMode>
)
