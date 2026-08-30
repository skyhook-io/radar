import { createElement, useCallback, useState } from 'react'
import { FolderOpen } from 'lucide-react'
import { useToast } from '../components/ui/Toast'
import { downloadFromUrl } from '../utils/desktop-download'
import { openFile, openFolder } from '../utils/desktop-open-folder'

/**
 * Downloads a file from an API URL and reports the outcome through a toast.
 *
 * In the desktop app the server saves straight to ~/Downloads and the success
 * toast offers to reveal it; in the browser the file downloads normally and
 * only failures are surfaced. Without this, a failed download left nothing on
 * screen but a console message.
 */
export function useFileDownload(): {
  downloading: boolean
  download: (url: string, filename: string) => Promise<void>
} {
  const [downloading, setDownloading] = useState(false)
  const { showSuccess, showError } = useToast()

  const download = useCallback(
    async (url: string, filename: string) => {
      setDownloading(true)
      try {
        const path = await downloadFromUrl(url, filename)
        if (path) {
          showSuccess(
            'File saved',
            path,
            {
              label: 'Show in Finder',
              icon: createElement(FolderOpen, { className: 'w-3.5 h-3.5' }),
              onClick: () => openFolder(path),
            },
            () => openFile(path),
          )
        }
      } catch (err) {
        showError('Download failed', err instanceof Error ? err.message : String(err))
      } finally {
        setDownloading(false)
      }
    },
    [showSuccess, showError],
  )

  return { downloading, download }
}
