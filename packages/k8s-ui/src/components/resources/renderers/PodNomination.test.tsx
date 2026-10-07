// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { PodRenderer } from './PodRenderer'
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

it('links an explicit nominated Node without presenting it as assigned placement', async () => {
  const element = document.createElement('div'); const root = createRoot(element); const onNavigate = vi.fn()
  try {
    await act(async () => root.render(<PodRenderer data={{metadata:{name:'pending',namespace:'app'},spec:{containers:[]},status:{phase:'Pending',nominatedNodeName:'candidate'}}} onCopy={() => {}} copied={null} onNavigate={onNavigate} />))
    expect(element.textContent).toContain('Nominated Node')
    const button = [...element.querySelectorAll('button')].find(b => b.textContent === 'candidate')
    expect(button).toBeDefined(); await act(async () => button!.click())
    expect(onNavigate).toHaveBeenCalledWith({kind:'nodes',group:'',namespace:'',name:'candidate'})
    expect(element.textContent).toContain('Pending')
  } finally { await act(async () => root.unmount()) }
})

it('does not infer a nominated Node from ordinary assigned placement', () => {
  const html = renderToStaticMarkup(<PodRenderer data={{metadata:{name:'running',namespace:'app'},spec:{nodeName:'assigned',containers:[]},status:{phase:'Running'}}} onCopy={() => {}} copied={null} />)
  expect(html).not.toContain('Nominated Node'); expect(html).toContain('assigned')
})
