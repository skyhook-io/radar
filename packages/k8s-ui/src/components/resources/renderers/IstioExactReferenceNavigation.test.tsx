// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { IstioDestinationRuleRenderer } from './IstioDestinationRuleRenderer'

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
 it.each([
  {host:'api.prod.svc.mesh.internal',domain:'mesh.internal',linked:true},
  {host:'api.prod.svc.cluster.local',domain:undefined,linked:false},
  {host:'api.prod.svc.external.test',domain:'mesh.internal',linked:false},
  {host:'api.prod',domain:'mesh.internal',linked:false},
 ])('links only observed Istio service domains: $host',async ({host,domain,linked})=>{
  const navigate=vi.fn()
  await rendered(<IstioDestinationRuleRenderer data={{metadata:{namespace:'app'},spec:{host}}} serviceDomain={domain} onNavigate={navigate}/>,async container=>{
   const link=[...container.querySelectorAll('button')].find(button=>button.textContent===host)
   expect(Boolean(link)).toBe(linked)
   if(link) {await click(container,host);expect(navigate).toHaveBeenCalledWith({kind:'services',group:'',namespace:'prod',name:'api'})}
  })
 })
})
