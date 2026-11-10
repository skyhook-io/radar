// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PodRenderer } from './PodRenderer'
import { getExecContainerNames } from '../resource-utils'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const running = { running: { startedAt: '2026-10-16T10:00:00Z' } }

const pod = {
  metadata: { name: 'app-with-sidecar', namespace: 'shop' },
  spec: {
    initContainers: [
      { name: 'migrate', image: 'busybox' },
      { name: 'log-shipper', image: 'fluent-bit:3', restartPolicy: 'Always' },
      { name: 'stopped-sidecar', image: 'busybox', restartPolicy: 'Always' },
    ],
    containers: [{ name: 'app', image: 'shop/app:1' }],
  },
  status: {
    phase: 'Running',
    initContainerStatuses: [
      { name: 'migrate', state: running, restartCount: 0 },
      { name: 'log-shipper', state: running, restartCount: 0, ready: true },
      { name: 'stopped-sidecar', state: { waiting: { reason: 'CrashLoopBackOff' } }, restartCount: 3 },
    ],
    containerStatuses: [{ name: 'app', state: running, restartCount: 0, ready: true }],
  },
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

function initCard(name: string): HTMLElement {
  const label = Array.from(container.querySelectorAll('span')).find((s) => s.textContent === name)
  if (!label) throw new Error(`no init container card for ${name}`)
  return label.closest('.rounded-lg') as HTMLElement
}

const terminalButton = (card: HTMLElement) => card.querySelector('svg.lucide-terminal')?.closest('button') ?? null

describe('PodRenderer native sidecar terminal', () => {
  it('offers Terminal on a running native sidecar only, and lists sidecars in the terminal', () => {
    const onOpenTerminal = vi.fn()
    act(() => {
      root.render(
        <PodRenderer data={pod} onCopy={() => undefined} copied={null} canExec canViewLogs onOpenTerminal={onOpenTerminal} />,
      )
    })

    expect(terminalButton(initCard('migrate'))).toBeNull()
    expect(terminalButton(initCard('stopped-sidecar'))).toBeNull()

    const sidecarTerminal = terminalButton(initCard('log-shipper'))
    expect(sidecarTerminal).not.toBeNull()
    act(() => sidecarTerminal!.click())

    expect(onOpenTerminal).toHaveBeenCalledWith({
      namespace: 'shop',
      podName: 'app-with-sidecar',
      containerName: 'log-shipper',
      containers: ['app', 'log-shipper', 'stopped-sidecar'],
    })
  })

  it('hides the sidecar Terminal without exec permission', () => {
    act(() => {
      root.render(
        <PodRenderer data={pod} onCopy={() => undefined} copied={null} canViewLogs onOpenTerminal={vi.fn()} />,
      )
    })
    expect(terminalButton(initCard('log-shipper'))).toBeNull()
  })
})

describe('PodRenderer sidecar files and image', () => {
  it('switches between a sidecar\'s files and its image, not the first container\'s', () => {
    let browser: { containers: string[]; initialContainer: string; onSwitchToImageFiles: (c?: string) => void } | undefined
    let imageBrowser: { image: string; onSwitchToPodFiles?: () => void } | undefined
    act(() => {
      root.render(
        <PodRenderer
          data={pod}
          onCopy={() => undefined}
          copied={null}
          canExec
          renderPodBrowser={(props) => { browser = props; return null }}
          renderImageBrowser={(props) => { imageBrowser = props; return null }}
        />,
      )
    })

    const card = initCard('log-shipper')
    act(() => (card.querySelector('svg.lucide-folder-open')!.closest('button') as HTMLButtonElement).click())
    expect(browser?.initialContainer).toBe('log-shipper')
    expect(browser?.containers).toEqual(['app', 'log-shipper', 'stopped-sidecar'])

    // The user switched the dialog to app before asking for image files.
    act(() => browser!.onSwitchToImageFiles('app'))
    expect(imageBrowser?.image).toBe('shop/app:1')
  })

  it('opens a sidecar\'s files from its image', () => {
    let browser: { initialContainer: string; onSwitchToImageFiles: (c?: string) => void } | undefined
    let imageBrowser: { image: string; onSwitchToPodFiles?: () => void } | undefined
    act(() => {
      root.render(
        <PodRenderer
          data={pod}
          onCopy={() => undefined}
          copied={null}
          canExec
          renderPodBrowser={(props) => { browser = props; return null }}
          renderImageBrowser={(props) => { imageBrowser = props; return null }}
        />,
      )
    })
    const card = initCard('log-shipper')
    act(() => (card.querySelector('svg.lucide-folder-open')!.closest('button') as HTMLButtonElement).click())
    act(() => browser!.onSwitchToImageFiles())
    expect(imageBrowser?.image).toBe('fluent-bit:3')
    act(() => imageBrowser!.onSwitchToPodFiles!())
    expect(browser?.initialContainer).toBe('log-shipper')
  })
})

describe('PodRenderer image to live files', () => {
  function renderWith(data: typeof pod) {
    let browser: { initialContainer: string } | undefined
    let imageBrowser: { image: string; onSwitchToPodFiles?: () => void } | undefined
    act(() => {
      root.render(
        <PodRenderer
          data={data}
          onCopy={() => undefined}
          copied={null}
          canExec
          renderPodBrowser={(props) => { browser = props; return null }}
          renderImageBrowser={(props) => { imageBrowser = props; return null }}
        />,
      )
    })
    return { getBrowser: () => browser, getImageBrowser: () => imageBrowser }
  }

  const openImage = (name: string) => {
    const card = initCard(name)
    const link = Array.from(card.querySelectorAll('button')).find((el) => el.textContent?.startsWith('Image:'))
    if (!link) throw new Error(`no image link on ${name}`)
    act(() => (link as HTMLElement).click())
  }

  it('does not send a stopped sidecar\'s image to its live files', () => {
    const { getBrowser, getImageBrowser } = renderWith(pod)
    // stopped-sidecar and migrate share the busybox image; neither is a running exec target.
    openImage('stopped-sidecar')
    expect(getImageBrowser()?.image).toBe('busybox')
    act(() => getImageBrowser()!.onSwitchToPodFiles!())
    expect(getBrowser()?.initialContainer).toBe('app')
  })

  it('keeps the live-files link for a running sidecar while the pod is still Pending', () => {
    const pending = { ...pod, status: { ...pod.status, phase: 'Pending' } }
    const { getBrowser, getImageBrowser } = renderWith(pending)
    openImage('log-shipper')
    expect(getImageBrowser()?.onSwitchToPodFiles).toBeDefined()
    act(() => getImageBrowser()!.onSwitchToPodFiles!())
    expect(getBrowser()?.initialContainer).toBe('log-shipper')
  })

  it('offers no live-files link in a Pending pod when nothing using the image runs', () => {
    const pending = { ...pod, status: { ...pod.status, phase: 'Pending' } }
    const { getImageBrowser } = renderWith(pending)
    openImage('stopped-sidecar')
    expect(getImageBrowser()?.onSwitchToPodFiles).toBeUndefined()
  })
})

describe('getExecContainerNames', () => {
  it('lists regular containers first, then native sidecars, never one-shot init containers', () => {
    expect(getExecContainerNames(pod)).toEqual(['app', 'log-shipper', 'stopped-sidecar'])
  })

  it('handles a pod without init containers', () => {
    expect(getExecContainerNames({ spec: { containers: [{ name: 'app' }] } })).toEqual(['app'])
  })
})
