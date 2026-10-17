// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { TektonTaskNode } from '../resource-utils-tekton'

const layout = vi.hoisted(() => ({ resolve: (_: unknown) => {} }))

vi.mock('elkjs/lib/elk.bundled.js', () => ({
  default: class {
    layout() {
      return new Promise((resolve) => { layout.resolve = resolve })
    }
  },
}))

// jsdom has no layout engine, so render each node's task status as text
// instead of going through React Flow's measured viewport.
vi.mock('@xyflow/react', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@xyflow/react')>()),
  ReactFlow: ({ nodes }: { nodes: Array<{ id: string; data: { task: TektonTaskNode } }> }) => (
    <ul>
      {nodes.map((n) => <li key={n.id} data-task={n.id}>{n.data.task.status}</li>)}
    </ul>
  ),
  ReactFlowProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  Controls: () => null,
  Panel: () => null,
}))

const { PipelineDagView } = await import('./PipelineDagView')

let element: HTMLDivElement
let root: Root

beforeEach(() => {
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})

afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
})

const tasks = (status: TektonTaskNode['status']): TektonTaskNode[] => [
  { name: 'build', dependsOn: [], status },
  { name: 'test', dependsOn: ['build'], status: 'pending' },
]

it('keeps a status update that lands while the layout is still running', async () => {
  await act(async () => root.render(<PipelineDagView tasks={tasks('running')} />))
  await act(async () => root.render(<PipelineDagView tasks={tasks('succeeded')} />))

  await act(async () => layout.resolve({ children: [{ id: 'build', x: 0, y: 0 }, { id: 'test', x: 300, y: 0 }] }))

  expect(element.querySelector('[data-task="build"]')?.textContent).toBe('succeeded')
})
