import { useEffect, useRef, useState } from 'react'
import { Check, Copy, ExternalLink } from 'lucide-react'
import { Tooltip } from './Tooltip'
import { showApiError } from './Toast'
import { copyText } from '../../utils/clipboard'

const ICON_BUTTON = 'inline-flex rounded p-1 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary'

/**
 * Open and Copy buttons after a hostname. With `url`, Open goes to it in a new
 * tab (the desktop app's link handler sends it to the system browser) and Copy
 * copies it. With only `hostname`, there's no Open and Copy copies the name.
 */
export function HostLinkActions({ url, hostname }: { url?: string; hostname?: string }) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])

  const text = url ?? hostname
  if (!text) return null
  const what = url ? 'URL' : 'hostname'

  const copy = () => {
    void copyText(text).then((ok) => {
      if (!ok) {
        showApiError(`Couldn't copy the ${what}`, 'The browser blocked clipboard access.')
        return
      }
      setCopied(true)
      clearTimeout(timer.current)
      timer.current = setTimeout(() => setCopied(false), 2000)
    })
  }

  return (
    <span className="inline-flex shrink-0 items-center">
      {url && (
        <Tooltip content={`Open ${url}`} position="top">
          <a
            href={url}
            target="_blank"
            rel="noopener noreferrer"
            aria-label={`Open ${url}`}
            className={ICON_BUTTON}
          >
            <ExternalLink className="h-3.5 w-3.5" />
          </a>
        </Tooltip>
      )}
      <Tooltip content={copied ? 'Copied' : `Copy ${what}`} position="top">
        <button
          type="button"
          onClick={copy}
          aria-label={`Copy ${what} ${text}`}
          className={ICON_BUTTON}
        >
          {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
        </button>
      </Tooltip>
    </span>
  )
}
