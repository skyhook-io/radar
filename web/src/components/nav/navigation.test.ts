import { describe, expect, it, vi } from 'vitest'
import { navigateFromPrimaryRail } from './navigation'

describe('navigateFromPrimaryRail', () => {
  it('closes an expanded investigation before showing the selected destination', () => {
    const calls: string[] = []

    navigateFromPrimaryRail(
      true,
      () => calls.push('close'),
      () => calls.push('navigate'),
    )

    expect(calls).toEqual(['close', 'navigate'])
  })

  it('keeps a docked investigation open across sidebar navigation', () => {
    const close = vi.fn()
    const navigate = vi.fn()

    navigateFromPrimaryRail(false, close, navigate)

    expect(close).not.toHaveBeenCalled()
    expect(navigate).toHaveBeenCalledOnce()
  })
})
