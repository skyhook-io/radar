/**
 * Triggers a browser download of a Blob.
 * When `overrideFn` is provided (e.g. for native desktop save dialogs),
 * it delegates to that instead of the default blob URL approach.
 * The override must handle its own errors (e.g. via toast notifications).
 */
export function triggerBlobDownload(
  blob: Blob,
  filename: string,
  overrideFn?: (blob: Blob, filename: string) => void,
): void {
  if (overrideFn) {
    overrideFn(blob, filename)
    return
  }

  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')

  a.href = url
  a.download = filename
  a.style.display = 'none'

  document.body.appendChild(a)
  a.click()

  // Delay cleanup so the browser has time to initiate the download before the blob URL is revoked.
  window.setTimeout(() => {
    URL.revokeObjectURL(url)
    a.remove()
  }, 1000)
}

/**
 * Triggers a file download of text content in the browser.
 * When `overrideFn` is provided (e.g. for native desktop save dialogs),
 * it delegates to that instead of the default blob URL approach.
 * The override must handle its own errors (e.g. via toast notifications).
 */
export function triggerDownload(
  content: string,
  mime: string,
  filename: string,
  overrideFn?: (content: string, mime: string, filename: string) => void,
): void {
  if (overrideFn) {
    overrideFn(content, mime, filename)
    return
  }
  triggerBlobDownload(new Blob([content], { type: mime }), filename)
}
