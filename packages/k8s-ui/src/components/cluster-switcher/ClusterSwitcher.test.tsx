// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ClusterSwitcher, type ClusterSwitcherHandle, type ClusterSwitcherItem } from './ClusterSwitcher'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let host: HTMLDivElement

const items: ClusterSwitcherItem[] = [
  { id: 'prod', name: 'prod' },
  { id: 'staging', name: 'staging' },
]

beforeEach(() => {
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
})
afterEach(() => {
  act(() => root.unmount())
  host.remove()
})

function open(props: { currentSelectable?: boolean }, onSelect: (item: ClusterSwitcherItem) => void) {
  const ref = { current: null as ClusterSwitcherHandle | null }
  act(() => root.render(
    <ClusterSwitcher ref={ref} currentId="prod" currentName="prod" items={items} onSelect={onSelect} searchable={false} {...props} />,
  ))
  act(() => ref.current?.open())
}

function row(name: string) {
  return [...host.querySelectorAll<HTMLButtonElement>('button[data-highlighted]')].find(b => b.textContent?.includes(name))!
}

describe('ClusterSwitcher current row', () => {
  it('is inert by default', () => {
    const onSelect = vi.fn()
    open({}, onSelect)
    expect(row('prod').disabled).toBe(true)
    act(() => row('prod').click())
    expect(onSelect).not.toHaveBeenCalled()
  })

  it('can be picked when the host asks for it', () => {
    const onSelect = vi.fn()
    open({ currentSelectable: true }, onSelect)
    expect(row('prod').disabled).toBe(false)
    act(() => row('prod').click())
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ id: 'prod' }))
  })
})
