import { type ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { ConfirmDialog } from './ConfirmDialog'

vi.mock('./DialogPortal', () => ({
  DialogPortal: ({ children }: { children: ReactNode }) => <>{children}</>,
}))

const props = {
  open: true,
  onClose: () => {},
  onConfirm: () => {},
  title: 'Share this investigation?',
  message: 'Organization members can read and continue this investigation.',
  confirmLabel: 'Share with organization',
}

describe('ConfirmDialog warning disclosure', () => {
  it('can omit redundant boilerplate without removing the actual disclosure or actions', () => {
    const html = renderToStaticMarkup(<ConfirmDialog {...props} variant="warning" showWarning={false} />)
    expect(html).toContain(props.message)
    expect(html).toContain(props.confirmLabel)
    expect(html).toContain('Cancel')
    expect(html).not.toContain('Please confirm you want to proceed')
  })

  it('preserves warning boilerplate by default for existing callers', () => {
    expect(renderToStaticMarkup(<ConfirmDialog {...props} variant="warning" />))
      .toContain('Please confirm you want to proceed with this action.')
  })

  it('preserves the irreversible-action warning for destructive callers', () => {
    expect(renderToStaticMarkup(<ConfirmDialog {...props} />))
      .toContain('This action cannot be undone.')
  })
})
