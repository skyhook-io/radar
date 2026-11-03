// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'
import cases from '../../../../../testdata/strimzi/connector-status.json'
import { getCellFilterValue } from './resource-utils'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()
let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove(); localStorage.clear() })

it('replaces the native Ready printer column with task-aware snapshot status', async () => {
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  const kind: APIResource = { group: 'kafka.strimzi.io', version: 'v1', kind: 'KafkaConnector', name: 'kafkaconnectors', namespaced: true, isCrd: true, verbs: ['get', 'list'] }
  const data = {...cases[1].resource, metadata: {...cases[1].resource.metadata, uid: 'fixture'}}
  const second = {...data, metadata:{...data.metadata,name:'zzz',uid:'second'},spec:{...data.spec,class:'AConnector'},status:{...data.status,connectorStatus:{...data.status.connectorStatus,tasks:Array.from({length:10},(_,id)=>({id,state:'RUNNING'}))}}}
  await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 48 }}><KeyboardShortcutProvider><ResourcesView printerTable={{kind:kind.name,group:kind.group,columns:[{name:'Ready',type:'string',jsonPath:'.status.conditions[?(@.type=="Ready")].status'}],cells:{fixture:['True']}}} namespaces={['lab']} apiResources={[kind]} resourceCounts={{ KafkaConnector: 2 }} selectedKindQuery={{ isLoading: false, data: [data,second], resourceName: kind.name, group: kind.group }} locationPathname="/resources/kafkaconnectors" locationSearch="?apiGroup=kafka.strimzi.io" onNavigate={() => {}} /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
  const table = host.querySelector('table')!
  expect(table).not.toBeNull()
  expect(table.textContent).toContain('Operator Snapshot')
  expect(table.textContent).toContain('Tasks failed')
  expect(table.textContent).not.toContain('Ready')
  const classHeader = [...table.querySelectorAll('th')].find(th=>th.textContent?.trim()==='Class')!
  act(() => classHeader.click())
  expect(table.querySelector('tbody tr')?.textContent).toContain('zzz')
  const tasksHeader = [...table.querySelectorAll('th')].find(th=>th.textContent?.trim()==='Tasks / Max')!
  act(() => tasksHeader.click())
  expect(table.querySelector('tbody tr')?.textContent).toContain('connector')
  expect(getCellFilterValue(data, 'status', 'kafkaconnectors')).toBe('Tasks failed')
})
