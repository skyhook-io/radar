// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import type { ManagerAnswer } from '../../types/core'
import { ManagedByChip } from './ManagedByChip'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const a = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'a' }
const b = { ...a, name: 'b' }
const object = { kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'web' }
const disagree: ManagerAnswer = { state: 'disagree', direct: { object, recorders: [b], claims: [{ manager: a, source: 'argo-tracking-id', check: 'omitted' }] } }

// A disagreement has no lead; its chip must still reach both managers.
it('opens a menu naming every candidate and what names each', async () => {
  const opened: string[] = []
  const element = document.createElement('div')
  document.body.append(element)
  const root = createRoot(element)
  await act(async () => root.render(<ManagedByChip manager={disagree} onOpen={ref => opened.push(ref.name)} />))
  await act(async () => { element.querySelector<HTMLButtonElement>('button[aria-haspopup="menu"]')!.click() })
  const items = [...element.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
  expect(items.map(i => i.textContent)).toEqual(['Application argocd/aThis metadata names it', 'Application argocd/bIts status lists this'])
  await act(async () => { items[1].click() })
  expect(opened).toEqual(['b'])
  await act(async () => root.unmount())
  element.remove()
})

it('a chip without a link is still reachable by keyboard', () => {
  expect(renderToStaticMarkup(<ManagedByChip manager={disagree} />)).toContain('tabindex="0"')
})
