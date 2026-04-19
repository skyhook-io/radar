import React from 'react'
import ReactDOM from 'react-dom/client'
import { RadarApp } from './RadarApp'
import { openExternal } from './utils/navigation'
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

// === Wails Desktop Clipboard ===
//
// Background: The desktop app uses a RedirectHandler that navigates the Wails
// webview from wails:// to http://localhost:<port>. After the redirect,
// window.runtime (Wails JS API) is no longer available. Clipboard operations
// must use navigator.clipboard and DOM events instead.
//
// What works and why:
//   Cmd+C / Cmd+X: Handled in keydown listener below. The Edit menu registers
//     these accelerators with nil callbacks (native responder chain), but WKWebView
//     does NOT dispatch a DOM copy/cut event from the native copy: selector.
//     The keydown event DOES reach JS, so we intercept it here.
//   Cmd+V: Handled by menu.go's explicit WindowExecJS callback which reads
//     navigator.clipboard.readText() and dispatches a synthetic paste event.
//   Right-click Copy/Cut (Monaco): Monaco calls document.execCommand('copy'/'cut'),
//     intercepted by the monkey-patch below.
//   Right-click Paste (Monaco): Not supported — Monaco calls navigator.clipboard
//     .readText() directly (not execCommand), and WKWebView blocks readText() from
//     page JS context. Use Cmd+V instead.

// Read selected text from Monaco if it has focus. Monaco uses virtual selection
// (not DOM selection), so window.getSelection() doesn't work — we access the
// editor instance exposed by YamlEditor.tsx.
function getMonacoSelection(): { text: string; editor: any } | null {
  const editor = (window as any).__radarMonacoEditor
  if (!editor?.hasTextFocus?.()) return null
  const sel = editor.getSelection()
  const model = editor.getModel()
  if (!sel || !model) return null
  const text = model.getValueInRange(sel)
  if (!text) return null
  return { text, editor }
}

function getSelectedText(): { text: string; monaco: { text: string; editor: any } | null } {
  const monaco = getMonacoSelection()
  if (monaco) return { text: monaco.text, monaco }
  const sel = window.getSelection()
  const text = sel ? sel.toString() : ''
  return { text, monaco: null }
}

function deleteMonacoSelection(editor: any): void {
  editor.pushUndoStop()
  editor.executeEdits('cut', [{ range: editor.getSelection(), text: '' }])
  editor.pushUndoStop()
}

function handleCopyOrCut(isCut: boolean): void {
  const { text, monaco } = getSelectedText()
  if (!text) return
  navigator.clipboard.writeText(text).catch((err) => { console.warn('[Radar] Clipboard write failed:', err) })
  if (isCut) {
    if (monaco) {
      deleteMonacoSelection(monaco.editor)
    } else {
      _origExecCommand('delete')
    }
  }
}

// Cmd+C/X: the menu's nil callback does NOT dispatch a DOM copy event.
document.addEventListener('keydown', (e) => {
  if (!(e.metaKey || e.ctrlKey)) return
  if (e.key !== 'c' && e.key !== 'x') return
  handleCopyOrCut(e.key === 'x')
}, true)

// Intercept copy/cut DOM events to handle Monaco's virtual selection.
// These fire from right-click -> Copy in some contexts. When a real
// ClipboardEvent is available, we write directly to e.clipboardData
// (synchronous, more reliable than the async clipboard API).
document.addEventListener('copy', (e: ClipboardEvent) => {
  const result = getMonacoSelection()
  if (result && e.clipboardData) {
    e.preventDefault()
    e.clipboardData.setData('text/plain', result.text)
  }
}, true)

document.addEventListener('cut', (e: ClipboardEvent) => {
  const result = getMonacoSelection()
  if (result && e.clipboardData) {
    e.preventDefault()
    e.clipboardData.setData('text/plain', result.text)
    deleteMonacoSelection(result.editor)
  }
}, true)

// Monkey-patch document.execCommand for Wails WebView compatibility.
// Handles copy/cut from Monaco's right-click context menu, and paste from
// any context that calls execCommand('paste').
const _origExecCommand = document.execCommand.bind(document)
document.execCommand = function (command: string, showUI?: boolean, value?: string) {
  if (command === 'copy' || command === 'cut') {
    handleCopyOrCut(command === 'cut')
    return true
  }
  if (command === 'paste') {
    navigator.clipboard.readText().then((text) => {
      if (!text) return
      const el = document.activeElement || document.body
      try {
        const dt = new DataTransfer()
        dt.setData('text/plain', text)
        const ev = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true })
        if (!el.dispatchEvent(ev)) return
      } catch (_e) { /* ClipboardEvent dispatch failed, fall back to insertText */ }
      _origExecCommand('insertText', false, text)
    }).catch((err) => { console.warn('[Radar] Paste failed:', err) })
    return true
  }
  return _origExecCommand(command, showUI, value)
} as typeof document.execCommand

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


// Standalone Radar binary: same-origin API, router at root. Library consumers
// (e.g. radar-hub-web) render <RadarApp apiBase="..." basename="..." /> instead.
ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <RadarApp />
  </React.StrictMode>
)
