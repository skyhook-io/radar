// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { ColumnPickerList } from './ColumnPickerList'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

describe('ColumnPickerList keyboard focus', () => {
  it.each([
    ['namespace', 'ArrowUp'],
    ['age', 'ArrowDown'],
  ])('does not restore stale focus after repeated boundary moves on %s', (key, direction) => {
    const host = document.createElement('div')
    document.body.append(host)
    const root = createRoot(host)
    const onReorder = vi.fn()
    const columns = ['name', 'namespace', 'age'].map(key => ({ key, label: key }))
    const render = () => act(() => root.render(<ColumnPickerList
      columns={columns}
      visibleColumns={new Set(['name', 'namespace', 'age'])}
      customColumnKeys={new Set()}
      onToggle={() => {}}
      onRemoveCustom={() => {}}
      onReorder={onReorder}
    />))
    try {
      render()
      const inputs = [...host.querySelectorAll<HTMLInputElement>('input')]
      const current = inputs[columns.findIndex(column => column.key === key)]
      const other = inputs[key === 'namespace' ? 2 : 1]
      if (!current || !other) throw new Error('Expected both movable checkboxes')
      current.focus()
      for (let i = 0; i < 3; i++) {
        act(() => current.dispatchEvent(new KeyboardEvent('keydown', { key: direction, altKey: true, bubbles: true })))
      }
      other.focus()
      render()
      expect(document.activeElement).toBe(other)
      expect(onReorder).not.toHaveBeenCalled()
    } finally {
      act(() => root.unmount())
      host.remove()
    }
  })
})
