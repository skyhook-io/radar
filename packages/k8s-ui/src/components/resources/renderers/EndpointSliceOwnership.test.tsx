// @vitest-environment jsdom
import {renderToStaticMarkup} from 'react-dom/server'
import {expect,it} from 'vitest'
import {EndpointSliceRenderer} from './EndpointSliceRenderer'
it('displays an unverified owner name and UID without a forward navigation link',()=>{
 const html=renderToStaticMarkup(<EndpointSliceRenderer data={{apiVersion:'discovery.k8s.io/v1',kind:'EndpointSlice',metadata:{name:'slice',namespace:'app',ownerReferences:[{apiVersion:'v1',kind:'Service',name:'old',uid:'deleted-owner'}]}}} onNavigate={()=>{}} />)
 expect(html).toContain('Declared Owner Service');expect(html).toContain('deleted-owner');expect([...new DOMParser().parseFromString(html, 'text/html').querySelectorAll('button')].some(button => button.textContent === 'old')).toBe(false)
})
it('retains standard label navigation independently from owner metadata',()=>{
 const html=renderToStaticMarkup(<EndpointSliceRenderer data={{apiVersion:'discovery.k8s.io/v1',kind:'EndpointSlice',metadata:{name:'slice',namespace:'app',labels:{'kubernetes.io/service-name':'published'}}}} onNavigate={()=>{}} />)
 expect(html).toContain('published');expect([...new DOMParser().parseFromString(html, 'text/html').querySelectorAll('button')].some(button => button.textContent === 'published')).toBe(true)
})
