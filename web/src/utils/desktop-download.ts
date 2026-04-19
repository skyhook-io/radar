// Desktop file-save utilities.
// In the desktop app (Wails), blob URL downloads are silently ignored by
// WKWebView / WebView2. These helpers route downloads through a backend
// endpoint that shows the native OS save dialog instead.

import { fetchJSON } from '../api/client'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../api/config'

let desktopCheck: Promise<boolean> | null = null

/** Returns true when running inside the desktop (Wails) app. Cached after first successful call. */
export function isDesktopApp(): Promise<boolean> {
  if (!desktopCheck) {
    desktopCheck = fetchJSON<{ isDesktop: boolean }>('/config')
      .then((d) => d.isDesktop ?? false)
      .catch(() => {
        desktopCheck = null // allow retry on next call
        return false
      })
  }
  return desktopCheck
}

/** Save text content via native save dialog. Returns the chosen file path, or throws with message 'cancelled' if the user dismisses the dialog. */
export async function desktopSaveFile(content: string, filename: string): Promise<string> {
  const res = await fetch(apiUrl('/desktop/save-file'), {
    method: 'POST',
    credentials: getCredentialsMode(),
    headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
    body: JSON.stringify({ content, filename }),
  })
  if (res.status === 204) throw new Error('cancelled')
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: 'Save failed' }))
    throw new Error(body.error ?? 'Save failed')
  }
  const body = await res.json()
  return body.path
}

/** Save a Blob via native save dialog. Returns the chosen file path, or throws with message 'cancelled' if the user dismisses the dialog. */
export async function desktopSaveBlob(blob: Blob, filename: string): Promise<string> {
  const contentBase64 = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onloadend = () => {
      const dataUrl = reader.result as string
      resolve(dataUrl.split(',')[1]) // strip "data:...;base64,"
    }
    reader.onerror = () => reject(new Error('Failed to read file'))
    reader.readAsDataURL(blob)
  })

  const res = await fetch(apiUrl('/desktop/save-file'), {
    method: 'POST',
    credentials: getCredentialsMode(),
    headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
    body: JSON.stringify({ contentBase64, filename }),
  })
  if (res.status === 204) throw new Error('cancelled')
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: 'Save failed' }))
    throw new Error(body.error ?? 'Save failed')
  }
  const body = await res.json()
  return body.path
}
