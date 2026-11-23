import { createElement, useState, useEffect, useCallback } from 'react'
import { FolderOpen } from 'lucide-react'
import { useToast } from '../components/ui/Toast'
import { isDesktopApp, desktopSaveFile, desktopSaveBlob } from '../utils/desktop-download'
import { openFile, openFolder } from '../utils/desktop-open-folder'

// Tracks whether this is the desktop app and reports a native save's outcome as a toast.
// User cancellation is silent.
function useDesktopSave(): { isDesktop: boolean; report: (save: Promise<string>) => void } {
  const [isDesktop, setIsDesktop] = useState(false)
  const { showSuccess, showError } = useToast()

  useEffect(() => {
    isDesktopApp().then(setIsDesktop)
  }, [])

  const report = useCallback((save: Promise<string>) => {
    save
      .then((path) => showSuccess(
        'File saved',
        path,
        {
          label: 'Show in Finder',
          icon: createElement(FolderOpen, { className: 'w-3.5 h-3.5' }),
          onClick: () => openFolder(path),
        },
        () => openFile(path),
      ))
      .catch((err: Error) => {
        if (err.message !== 'cancelled') {
          showError('Save failed', err.message)
        }
      })
  }, [showSuccess, showError])

  return { isDesktop, report }
}

/**
 * Returns a download override function when running in the desktop app,
 * or undefined when running in a browser (so the default blob URL approach is used).
 * The returned function shows toast notifications for success/failure and silently handles user cancellation.
 */
export function useDesktopDownload(): ((content: string, mime: string, filename: string) => void) | undefined {
  const { isDesktop, report } = useDesktopSave()
  const download = useCallback((content: string, _mime: string, filename: string) => {
    report(desktopSaveFile(content, filename))
  }, [report])

  if (!isDesktop) return undefined
  return download
}

/** Blob variant of useDesktopDownload, for binary files such as exported images. */
export function useDesktopBlobDownload(): ((blob: Blob, filename: string) => void) | undefined {
  const { isDesktop, report } = useDesktopSave()
  const download = useCallback((blob: Blob, filename: string) => {
    report(desktopSaveBlob(blob, filename))
  }, [report])

  if (!isDesktop) return undefined
  return download
}
