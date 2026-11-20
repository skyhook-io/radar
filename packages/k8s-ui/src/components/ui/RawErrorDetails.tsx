import { useState } from 'react'

import { copyText } from '../../utils/clipboard'
import { Collapse, CollapseChevron, useDisclosure } from './Collapse'

export function RawErrorDetails({ detail, onOpenChange }: { detail: string; onOpenChange?: (open: boolean) => void }) {
  const [open, setOpen] = useState(false)
  const [copyStatus, setCopyStatus] = useState<'idle' | 'copied' | 'unavailable'>('idle')
  const disclosure = useDisclosure(open)

  return (
    <>
      <button
        type="button"
        {...disclosure.buttonProps}
        onClick={() => { setOpen(!open); onOpenChange?.(!open) }}
        className="flex items-center gap-1 text-xs"
      >
        <CollapseChevron open={open} inheritColor /> Error details
      </button>
      <Collapse open={open} id={disclosure.panelId}>
        <button
          type="button"
          onClick={async () => setCopyStatus(await copyText(detail) ? 'copied' : 'unavailable')}
          className="mt-2 rounded border border-current px-2 py-1 text-xs"
        >
          {copyStatus === 'copied' ? 'Copied' : 'Copy raw error'}
        </button>
        <p role="status" className={copyStatus === 'unavailable' ? 'mt-1 text-xs' : 'sr-only'}>
          {copyStatus === 'unavailable' && 'Select the error text below to copy it.'}
        </p>
        <pre tabIndex={0} className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-words text-xs">{detail}</pre>
      </Collapse>
    </>
  )
}
