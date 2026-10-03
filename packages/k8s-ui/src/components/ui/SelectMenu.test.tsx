// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { SelectMenu } from './SelectMenu'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

it('keeps one option tabbable when the options shrink while the menu is open', async () => {
  const element = document.createElement('div')
  document.body.append(element)
  const root = createRoot(element)
  const render = (options: { value: string; label: string }[]) =>
    root.render(<SelectMenu value="c" onChange={() => {}} options={options} ariaLabel="Source" />)
  await act(async () => render([{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }, { value: 'c', label: 'C' }]))
  await act(async () => { element.querySelector<HTMLButtonElement>('button[aria-haspopup="listbox"]')!.click() })
  expect(element.querySelectorAll('[role="option"]')).toHaveLength(3)

  await act(async () => render([{ value: 'a', label: 'A' }]))
  const tabbable = element.querySelectorAll('[role="option"][tabindex="0"]')
  expect(tabbable).toHaveLength(1)
  expect(tabbable[0].textContent).toBe('A')
  await act(async () => root.unmount())
  element.remove()
})
