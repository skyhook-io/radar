// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { PodRenderer } from './PodRenderer'

const data = { metadata: {name:'worker',namespace:'app'}, spec:{containers:[],priority:1000,priorityClassName:'urgent',runtimeClassName:'sandbox'},status:{phase:'Pending'} }

it('navigates explicit scheduling class names with their canonical API groups and cluster scope', async()=>{
 const onNavigate=vi.fn();const node=document.createElement('div');document.body.append(node);const root=createRoot(node)
 try{
  await act(async()=>{root.render(<PodRenderer data={data} onCopy={()=>{}} copied={null} onNavigate={onNavigate}/>)})
  for(const [name,kind,group] of [['urgent','priorityclasses','scheduling.k8s.io'],['sandbox','runtimeclasses','node.k8s.io']]){
   const button=Array.from(node.querySelectorAll('button')).find(b=>b.textContent===name);expect(button).toBeDefined();await act(async()=>{button!.click()});expect(onNavigate).toHaveBeenLastCalledWith({kind,group,namespace:'',name})
  }
 }finally{await act(async()=>{root.unmount()});node.remove()}
})
it('does not infer a class reference from numeric priority or absent class fields',()=>{
 const html=renderToStaticMarkup(<PodRenderer data={{...data,spec:{containers:[],priority:1000}}} onCopy={()=>{}} copied={null}/>)
 expect(html).not.toContain('Priority Class');expect(html).not.toContain('Runtime Class')
})
