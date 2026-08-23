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

export function installWailsClipboardShim(): void {
  const _origExecCommand = document.execCommand.bind(document)

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
  //
  // Only Monaco needs the copy/cut hijack: its virtual selection is invisible
  // to the native command. Every other caller (notably the hidden-textarea
  // fallback in @skyhook-io/k8s-ui's copyText, which is how copy works at all
  // on insecure origins where navigator.clipboard doesn't exist) must reach
  // the native command and see its real return value — hijacking those routes
  // them back into the async Clipboard API and fabricates success.
  document.execCommand = function (command: string, showUI?: boolean, value?: string) {
    if (command === 'copy' || command === 'cut') {
      const monaco = getMonacoSelection()
      if (!monaco) return _origExecCommand(command, showUI, value)
      navigator.clipboard.writeText(monaco.text).catch((err) => { console.warn('[Radar] Clipboard write failed:', err) })
      if (command === 'cut') deleteMonacoSelection(monaco.editor)
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
        } catch { /* ClipboardEvent dispatch failed, fall back to insertText */ }
        _origExecCommand('insertText', false, text)
      }).catch((err) => { console.warn('[Radar] Paste failed:', err) })
      return true
    }
    return _origExecCommand(command, showUI, value)
  } as typeof document.execCommand
}
