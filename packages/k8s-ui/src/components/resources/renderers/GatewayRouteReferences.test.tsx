// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { HTTPRouteRenderer } from './HTTPRouteRenderer'
import { GRPCRouteRenderer } from './GRPCRouteRenderer'
import { SimpleRouteRenderer } from './SimpleRouteRenderer'

it.each(['HTTPRoute','GRPCRoute','TCPRoute','TLSRoute','UDPRoute'] as const)('preserves backend and parent identities in %s click paths', async (kind) => {
  const onNavigate=vi.fn()
  const backend={name:'custom-backend',kind:'Widget',group:'relationships.radar.test',namespace:'backends'}
  const parent={name:'mesh-parent',kind:'Service',group:'',namespace:'mesh'}
  const data={status:{parents:[{parentRef:parent,conditions:[{type:'Accepted',status:'False',reason:'Rejected'}]}]},metadata:{name:'route',namespace:'app'},spec:{parentRefs:[parent],rules:[{backendRefs:[backend,{name:'default-service',port:8080}], ...(kind==='HTTPRoute'?{filters:[{type:'RequestMirror',requestMirror:{backendRef:{...backend,name:'mirror'}}}]}:{})}]}}
  const node=document.createElement('div');document.body.append(node);const root=createRoot(node)
  try {
    await act(async()=>{root.render(kind==='HTTPRoute'?<HTTPRouteRenderer data={data} onNavigate={onNavigate}/>:kind==='GRPCRoute'?<GRPCRouteRenderer data={data} onNavigate={onNavigate}/>:<SimpleRouteRenderer kind={kind} data={data} onNavigate={onNavigate}/>)})
    expect(node.textContent).toContain('Parents')
    expect(node.textContent).toContain('Parent "mesh-parent"')
    expect(node.textContent).not.toContain('Gateway "mesh-parent"')
    for(const [name,ref] of [['custom-backend',backend],['default-service',{kind:'Service',group:'',namespace:'app',name:'default-service'}],['mesh-parent',parent]] as const){
      const button=Array.from(node.querySelectorAll('button')).find(b=>b.textContent?.includes(name));expect(button,`button ${name}`).toBeDefined()
      await act(async()=>{button!.click()})
      expect(onNavigate).toHaveBeenLastCalledWith({kind:ref.kind,group:ref.group,namespace:ref.namespace,name:ref.name})
    }
    if(kind==='HTTPRoute'){
      const button=Array.from(node.querySelectorAll('button')).find(b=>b.textContent?.includes('mirror'));expect(button).toBeDefined();await act(async()=>{button!.click()});expect(onNavigate).toHaveBeenLastCalledWith({...backend,name:'mirror'})
    }
  } finally {await act(async()=>{root.unmount()});node.remove()}
})
