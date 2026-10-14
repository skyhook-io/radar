// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ActiveFilterBar, type ActiveFilterBarProps } from './ActiveFilterBar'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

let root: Root | null = null
let element: HTMLDivElement

async function render(overrides: Partial<ActiveFilterBarProps> = {}) {
  const props: ActiveFilterBarProps = {
    columnFilters: {},
    columnFilterExcludes: {},
    problemFilters: [],
    labelSelector: '',
    onClearColumn: vi.fn(),
    onClearProblems: vi.fn(),
    onClearLabels: vi.fn(),
    ...overrides,
  }
  element = document.createElement('div')
  root = createRoot(element)
  await act(async () => root!.render(<ActiveFilterBar {...props} />))
  return props
}

function buttonWithText(text: string): HTMLButtonElement {
  const button = [...element.querySelectorAll('button')].find((b) => b.textContent?.includes(text))
  if (!button) throw new Error(`no button containing "${text}"`)
  return button
}

afterEach(async () => {
  await act(async () => root?.unmount())
  root = null
})

describe('ActiveFilterBar', () => {
  it('renders nothing without active filters or a restore', async () => {
    await render({ columnFilters: { status: [] } })
    expect(element.innerHTML).toBe('')
  })

  it('renders one chip per active filter, marking excluded columns', async () => {
    await render({
      columnFilters: { status: ['Running', 'Pending'], node: ['n1'], empty: [] },
      columnFilterExcludes: { node: true },
      problemFilters: ['crashloop'],
      labelSelector: 'app=nginx',
    })
    expect(element.querySelectorAll('button')).toHaveLength(4)
    expect(element.textContent).toContain('status: Running, Pending')
    expect(element.textContent).toContain('node: not n1')
    expect(element.textContent).toContain('Problems: crashloop')
    expect(element.textContent).toContain('app=nginx')
    expect(element.textContent).not.toContain('restored')
  })

  it('calls the matching clear handler when a chip is clicked', async () => {
    const props = await render({ columnFilters: { status: ['Running'] }, problemFilters: ['oom'], labelSelector: 'tier=web' })
    await act(async () => buttonWithText('status:').click())
    await act(async () => buttonWithText('Problems:').click())
    await act(async () => buttonWithText('tier=web').click())
    expect(props.onClearColumn).toHaveBeenCalledWith('status')
    expect(props.onClearProblems).toHaveBeenCalledOnce()
    expect(props.onClearLabels).toHaveBeenCalledOnce()
  })

  it('shows the restored line only when restored, with Clear calling onClearAll', async () => {
    const onClearAll = vi.fn()
    await render({ restored: true, onClearAll, columnFilters: { status: ['Running'] } })
    expect(element.textContent).toContain('Filters restored from your last visit')
    await act(async () => buttonWithText('Clear').click())
    expect(onClearAll).toHaveBeenCalledOnce()
  })
})
