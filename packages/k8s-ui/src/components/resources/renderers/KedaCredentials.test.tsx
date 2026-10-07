// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { KedaTriggerAuthRenderer } from './KedaTriggerAuthRenderer'
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const spec = {secretTargetRef:[{parameter:'token',name:'direct',key:'token'}],gcpSecretManager:{credentials:{clientSecret:{valueFrom:{secretKeyRef:{name:'iam',key:'credentials.json'}}}}}}

it('links the actual nested GCP credential Secret in the authentication namespace', async () => {
 const element=document.createElement('div');const root=createRoot(element);const onNavigate=vi.fn()
 try {
  await act(async()=>root.render(<KedaTriggerAuthRenderer data={{kind:'TriggerAuthentication',metadata:{namespace:'app'},spec}} onNavigate={onNavigate}/>))
  const button=[...element.querySelectorAll('button')].find(b=>b.textContent==='iam');expect(button).toBeDefined();await act(async()=>button!.click())
  expect(onNavigate).toHaveBeenCalledWith({kind:'secrets',group:'',namespace:'app',name:'iam'});expect(element.textContent).toContain('credentials.json')
 }finally{await act(async()=>root.unmount())}
})

it('retains cluster-auth names without inventing an operator credential namespace',()=>{
 const html=renderToStaticMarkup(<KedaTriggerAuthRenderer data={{kind:'ClusterTriggerAuthentication',metadata:{name:'cluster'},spec}} onNavigate={()=>{}}/>);const doc=new DOMParser().parseFromString(html,'text/html')
 expect(doc.body.textContent).toContain('iam');expect(doc.body.textContent).toContain('direct');expect([...doc.querySelectorAll('button')].some(b=>b.textContent==='iam'||b.textContent==='direct')).toBe(false)
})

it('does not accept the previously invented shallow GCP reference shape',()=>{
 const html=renderToStaticMarkup(<KedaTriggerAuthRenderer data={{kind:'TriggerAuthentication',metadata:{namespace:'app'},spec:{gcpSecretManager:{credentials:{clientSecret:{name:'fictional'}}}}}}/>);expect(html).not.toContain('fictional')
})
