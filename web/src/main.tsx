import React from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider, MutationCache, QueryCache } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import { ToastProvider, showApiError, showApiSuccess } from './components/ui/Toast'
import { ThemeProvider } from './context/ThemeContext'
import { openExternal } from './utils/navigation'
import './index.css'

// Intercept external link clicks in the Wails desktop app.
// <a target="_blank"> is swallowed by WKWebView/WebView2 — route through openExternal()
// which calls the backend /api/desktop/open-url endpoint to open in the system browser.
window.addEventListener('click', (e: MouseEvent) => {
  const anchor = (e.target as HTMLElement).closest?.('a[href]') as HTMLAnchorElement | null
  if (!anchor) return
  const href = anchor.href
  if (!href || href.startsWith(window.location.origin) || href.startsWith('/') || href.startsWith('#')) return
  // External URL — open via system browser
  e.preventDefault()
  openExternal(href)
})

// Patch document.execCommand('paste') for Wails WebView compatibility.
// WKWebView blocks execCommand('paste') from non-native UI elements (like Monaco's
// right-click context menu). This intercept reads from the async clipboard API and
// inserts text into the active element. The async nature means it won't return
// synchronously, but the text will arrive in the editor shortly after the click.
const _origExecCommand = document.execCommand.bind(document)
document.execCommand = function (command: string, showUI?: boolean, value?: string) {
  if (command === 'paste') {
    navigator.clipboard.readText().then((text) => {
      if (!text) return
      const el = document.activeElement || document.body
      try {
        const dt = new DataTransfer()
        dt.setData('text/plain', text)
        const ev = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true })
        if (!el.dispatchEvent(ev)) return
      } catch (_e) { /* fallback below */ }
      _origExecCommand('insertText', false, text)
    }).catch((err) => { console.warn('[Radar] Paste failed:', err) })
    return true
  }
  return _origExecCommand(command, showUI, value)
} as typeof document.execCommand

// Mouse back/forward button navigation for desktop webview.
// On Windows (WebView2/Chromium), these events fire natively — this handles them.
// On macOS (WKWebView), these events never reach JS — handled by native NSEvent monitor in mouse_darwin.go.
// On Linux (webkit2gtk), behavior varies by version — this catches it when supported.
window.addEventListener('auxclick', (e: MouseEvent) => {
  if (e.button === 3) {
    e.preventDefault()
    window.history.back()
  } else if (e.button === 4) {
    e.preventDefault()
    window.history.forward()
  }
})

// Type the meta property for mutations
declare module '@tanstack/react-query' {
  interface Register {
    mutationMeta: {
      errorMessage?: string      // e.g., "Failed to delete resource"
      successMessage?: string    // e.g., "Resource deleted"
      successDetail?: string     // e.g., "Pod 'nginx' removed"
    }
  }
}

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
  mutationCache: new MutationCache({
    onError: (error, _variables, _context, mutation) => {
      // Only show toast if errorMessage is explicitly provided in meta
      // This allows mutations to opt-out by not providing meta (e.g., context switch has its own dialog)
      const message = mutation.options.meta?.errorMessage
      if (message) {
        showApiError(message, error.message)
      }
    },
    onSuccess: (_data, _variables, _context, mutation) => {
      const message = mutation.options.meta?.successMessage
      if (message) {
        showApiSuccess(message, mutation.options.meta?.successDetail)
      }
    },
  }),
  queryCache: new QueryCache({
    onError: (error, query) => {
      // Log background refetch failures (when stale data exists)
      if (query.state.data !== undefined) {
        console.warn('[Background sync failed]', query.queryKey, error.message)
      }
    },
  }),
})

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter>
      <ThemeProvider>
        <QueryClientProvider client={queryClient}>
          <ToastProvider>
            <App />
          </ToastProvider>
        </QueryClientProvider>
      </ThemeProvider>
    </BrowserRouter>
  </React.StrictMode>
)
