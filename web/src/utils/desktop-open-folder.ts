import { apiUrl, getAuthHeaders, getCredentialsMode } from '../api/config'

/** Reveal a file in the system file manager (Finder on macOS, Explorer on Windows). */
export function openFolder(path: string): void {
  fetch(apiUrl('/desktop/open-folder'), {
    method: 'POST',
    credentials: getCredentialsMode(),
    headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
    body: JSON.stringify({ path }),
  }).catch((err) => console.warn('[desktop] Failed to open folder:', err))
}

/** Open a file with the system default application. */
export function openFile(path: string): void {
  fetch(apiUrl('/desktop/open-file'), {
    method: 'POST',
    credentials: getCredentialsMode(),
    headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
    body: JSON.stringify({ path }),
  }).catch((err) => console.warn('[desktop] Failed to open file:', err))
}
