// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { CAPIClusterRenderer } from './CAPIClusterRenderer'

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
 it('uses the v1beta2 ClusterClass reference namespace',async()=>{
  const navigate=vi.fn()
  await rendered(<CAPIClusterRenderer data={{metadata:{namespace:'app'},spec:{topology:{classRef:{name:'standard',namespace:'classes'}}}}} onNavigate={navigate}/>,async container=>{
   await click(container,'standard');expect(navigate).toHaveBeenCalledWith({kind:'clusterclasses',group:'cluster.x-k8s.io',namespace:'classes',name:'standard'})
  })
 })
})
