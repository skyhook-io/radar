import type { KeyboardEvent } from 'react'

export function trapConnectionConfirmationFocus(
  event: KeyboardEvent<HTMLElement>
) {
  if (event.key !== 'Tab') return
  const dialog = (event.target as HTMLElement).closest('[role="dialog"]')
  const buttons = dialog?.querySelectorAll<HTMLButtonElement>(
    'button:not(:disabled)'
  )
  if (!buttons?.length) {
    event.preventDefault()
    return
  }
  const first = buttons[0]
  const last = buttons[buttons.length - 1]
  if (event.shiftKey && (event.target === first || event.target === dialog)) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && event.target === last) {
    event.preventDefault()
    first.focus()
  }
}
