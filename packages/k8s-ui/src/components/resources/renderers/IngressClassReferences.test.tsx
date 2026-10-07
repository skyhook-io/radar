// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { IngressRenderer } from './IngressRenderer'
import { IngressClassRenderer } from './IngressClassRenderer'
import { ingressClassParametersResourceRef } from '../../../utils/ingress-class-references'

it('uses the parameters reference API group and explicit scope without an apiVersion guess',()=>{
 expect(ingressClassParametersResourceRef({kind:'IngressClassParams',apiGroup:'elbv2.k8s.aws',name:'shared'})).toEqual({kind:'IngressClassParams',group:'elbv2.k8s.aws',namespace:'',name:'shared'})
 expect(ingressClassParametersResourceRef({kind:'ConfigMap',scope:'Namespace',namespace:'controller',name:'settings'})).toEqual({kind:'ConfigMap',group:'',namespace:'controller',name:'settings'})
 expect(ingressClassParametersResourceRef({kind:'ConfigMap',scope:'Namespace',name:'settings'})).toBeNull()
})
it('clicks through explicit Ingress class and both parameter scopes, while legacy annotations remain text',async()=>{
 const onNavigate=vi.fn();const node=document.createElement('div');document.body.append(node);const root=createRoot(node)
 const click=async(name:string)=>{const b=Array.from(node.querySelectorAll('button')).find(b=>b.textContent===name);expect(b).toBeDefined();await act(async()=>{b!.click()})}
 try{
  await act(async()=>{root.render(<IngressRenderer data={{metadata:{namespace:'app',annotations:{'kubernetes.io/ingress.class':'old-controller'}},spec:{ingressClassName:'public'}}} onNavigate={onNavigate}/>)})
  await click('public');expect(onNavigate).toHaveBeenLastCalledWith({kind:'ingressclasses',group:'networking.k8s.io',namespace:'',name:'public'})
  await act(async()=>{root.render(<IngressRenderer data={{metadata:{annotations:{'kubernetes.io/ingress.class':'old-controller'}},spec:{}}} onNavigate={onNavigate}/>)})
  expect(node.textContent).toContain('old-controller');expect(Array.from(node.querySelectorAll('button')).some(b=>b.textContent==='old-controller')).toBe(false)
  for(const parameters of [{name:'shared',kind:'IngressClassParams',apiGroup:'elbv2.k8s.aws'},{name:'settings',kind:'ConfigMap',scope:'Namespace' as const,namespace:'controller'}]){
   await act(async()=>{root.render(<IngressClassRenderer data={{spec:{controller:'example.test/ingress',parameters}}} onNavigate={onNavigate}/>)})
   await click(parameters.name);expect(onNavigate).toHaveBeenLastCalledWith(ingressClassParametersResourceRef(parameters))
  }
 }finally{await act(async()=>{root.unmount()});node.remove()}
})
