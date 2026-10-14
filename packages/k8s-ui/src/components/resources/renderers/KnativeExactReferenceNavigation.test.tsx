// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { KnativeReferenceLink } from './KnativeReferenceLink'
import { TriggerRenderer } from './KnativeEventingRenderer'

async function rendered(element: ReactNode, check: (container: HTMLDivElement) => Promise<void> | void) {
 Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
 const container = document.createElement('div')
 const root = createRoot(container)
 try { await act(async () => root.render(element)); await check(container) }
 finally { await act(async () => root.unmount()); Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false }) }
}
async function click(container: HTMLDivElement, text: string) {
 const button = [...container.querySelectorAll('button')].find(button => button.textContent === text)
 expect(button, `missing link ${text}`).toBeDefined()
 await act(async () => button!.click())
}

describe('exact reference navigation', () => {
 it('follows the Trigger broker reconciler namespace/name contract',async()=>{
  const navigate=vi.fn()
  await rendered(<TriggerRenderer data={{metadata:{namespace:'app'},spec:{brokerRef:{namespace:'events',name:'actual',kind:'Service',apiVersion:'v1'}}}} onNavigate={navigate}/>,async container=>{
   await click(container,'actual');expect(navigate).toHaveBeenCalledWith({kind:'brokers',group:'eventing.knative.dev',namespace:'events',name:'actual'})
  })
 })

 it.each([
  {apiVersion:'messaging.knative.dev/v1',kind:'InMemoryChannel',name:'events'},
  {group:'messaging.knative.dev',kind:'InMemoryChannel',name:'events'},
 ])('uses the actual Knative target group and kind: %j', async reference => {
  const navigate=vi.fn()
  await rendered(<KnativeReferenceLink reference={reference} namespace="app" onNavigate={navigate}/>,async container=>{
   await click(container,'events')
   expect(navigate).toHaveBeenCalledWith({kind:'inmemorychannels',group:'messaging.knative.dev',namespace:'app',name:'events'})
  })
 })
 it.each([
  {kind:'Service',name:'events'},
  {apiVersion:'v1',group:'serving.knative.dev',kind:'Service',name:'events'},
 ])('leaves incomplete or conflicting Knative identity as text: %j', async reference=>{
  await rendered(<KnativeReferenceLink reference={reference} namespace="app" onNavigate={vi.fn()}/>,container=>{
   expect(container.textContent).toContain('events');expect(container.querySelector('button')).toBeNull()
  })
 })
})
