// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { ContourHTTPProxyRenderer } from './ContourHTTPProxyRenderer'

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
 it.each([['tls/', 'tls'], ['/', 'app']])('resolves Contour TLS secret prefix %s to namespace %s',async(prefix, namespace)=>{
  const navigate=vi.fn()
  await rendered(<ContourHTTPProxyRenderer data={{metadata:{namespace:'app'},spec:{virtualhost:{fqdn:'example.test',tls:{secretName:`${prefix}server`,clientValidation:{caSecret:`${prefix}ca`,crlSecret:`${prefix}crl`}}}}}} onNavigate={navigate}/>,async container=>{
   for(const name of ['server','ca','crl']) {await click(container,`${prefix}${name}`);expect(navigate).toHaveBeenLastCalledWith({kind:'secrets',group:'',namespace,name})}
  })
 })
 it('links valid long Contour Secret names',async()=>{
  const navigate=vi.fn()
  const name='a'.repeat(70)
  await rendered(<ContourHTTPProxyRenderer data={{metadata:{namespace:'app'},spec:{virtualhost:{tls:{secretName:name}}}}} onNavigate={navigate}/>,async container=>{
   await click(container,name);expect(navigate).toHaveBeenCalledWith({kind:'secrets',group:'',namespace:'app',name})
  })
 })
})
