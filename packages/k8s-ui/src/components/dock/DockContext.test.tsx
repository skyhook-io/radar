// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'
import { DockProvider, useDock, useOpenTerminal, type DockContextValue } from './DockContext'
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

it('reuses normal terminals and separates explicit executables and cluster identities', async () => {
  let dock!: DockContextValue
  let open!: ReturnType<typeof useOpenTerminal>
  function Host() { dock = useDock(); open = useOpenTerminal(); return null }
  const host = document.createElement('div'); document.body.appendChild(host); const root = createRoot(host)
  await act(async () => root.render(<DockProvider><Host /></DockProvider>))
  const base = { namespace: 'app', podName: 'worker', containerName: 'app', containers: ['app'], orgId: 'org', clusterId: 'first' }
  try {
    await act(async () => open(base)); await act(async () => open(base))
    expect(dock.tabs).toHaveLength(1); expect(dock.tabs[0].title).toBe('worker/app')
    await act(async () => open({ ...base, shell: 'psql', title: 'Database', sessionNote: 'Primary' }))
    expect(dock.tabs).toHaveLength(2)
    expect(dock.tabs[1]).toMatchObject({ shell: 'psql', title: 'Database', sessionNote: 'Primary' })
    const commandId = dock.tabs[1].id
    await act(async () => open({ ...base, shell: 'psql', title: 'Database standby', sessionNote: 'Standby' }))
    expect(dock.tabs).toHaveLength(2)
    expect(dock.tabs[1]).toMatchObject({ id: commandId, title: 'Database standby', sessionNote: 'Standby' })
    const defaultId = dock.tabs[0].id
    await act(async () => dock.setTabTitle(defaultId, 'My shell'))
    await act(async () => open(base))
    expect(dock.tabs[0].title).toBe('My shell')
    await act(async () => open({ ...base, clusterId: 'second', shell: 'psql' })); expect(dock.tabs).toHaveLength(3)
    await act(async () => open({ ...base, orgId: 'other' })); expect(dock.tabs).toHaveLength(4)
  } finally { await act(async () => root.unmount()); host.remove() }
})
