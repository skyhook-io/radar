// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { ConfigBlock } from './CNPGOperator'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('summarizes monitoring queries, then expands newline-preserving SQL with explicit truncation and inspection', () => {
  const queries = `connections:\n  query: |\n    SELECT count(*)\n    FROM pg_stat_activity;\nlong_query:\n  query: '${'x'.repeat(8200)}'\n`
  const inspect = vi.fn()
  const host = document.createElement('div')
  const root = createRoot(host)
  act(() => root.render(<ConfigBlock config={{ kind: 'ConfigMap', namespace: 'operator', name: 'monitoring', purpose: 'monitoring', exists: true, readable: true, data: { queries } }} onInspect={inspect} />))
  expect(host.textContent).toContain('2 queries · ConfigMap monitoring')
  const fold = [...host.querySelectorAll('button')].find((b) => b.textContent?.includes('Query definitions'))!
  expect(fold.getAttribute('aria-expanded')).toBe('false')
  act(() => fold.click())
  const preview = host.querySelector('pre')!
  expect(preview.textContent).toBe(queries.slice(0, 8000))
  expect(preview.classList.contains('whitespace-pre-wrap')).toBe(true)
  expect(host.textContent).toContain(`Preview truncated to 8,000 of ${queries.length.toLocaleString()} characters`)
  const link = [...host.querySelectorAll('button')].find((b) => b.textContent?.includes('Inspect ConfigMap'))!
  act(() => link.click())
  expect(inspect).toHaveBeenCalledWith({ kind: 'configmaps', group: '', namespace: 'operator', name: 'monitoring' })
  act(() => root.unmount())
})
