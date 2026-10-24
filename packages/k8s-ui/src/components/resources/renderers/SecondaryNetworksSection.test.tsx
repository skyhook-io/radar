// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { SecondaryNetworksSection } from './SecondaryNetworksSection'
import {
  MULTUS_NETWORKS_ANNOTATION,
  MULTUS_NETWORK_STATUS_ANNOTATION,
  type NetworkAttachmentLookups,
} from '../../../utils/multus'

const pod = (annotations: Record<string, string>) => ({ metadata: { name: 'worker', namespace: 'apps', annotations } })

const status = (entries: unknown[]) => JSON.stringify(entries)

async function render(element: React.ReactElement, assert: (container: HTMLElement) => Promise<void> | void) {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const container = document.createElement('div')
  const root = createRoot(container)
  try {
    await act(async () => root.render(element))
    await assert(container)
  } finally {
    await act(async () => root.unmount())
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
  }
}

const buttons = (container: HTMLElement) => [...container.querySelectorAll('button')]

describe('SecondaryNetworksSection', () => {
  it('renders nothing for a Pod with no Multus annotations or only its default network', async () => {
    await render(<SecondaryNetworksSection pod={pod({})} />, container => {
      expect(container.textContent).toBe('')
    })
    await render(
      <SecondaryNetworksSection pod={pod({
        [MULTUS_NETWORK_STATUS_ANNOTATION]: status([{ name: 'ovn-kubernetes', interface: 'eth0', ips: ['10.131.0.26'], default: true }]),
      })} />,
      container => { expect(container.textContent).toBe('') },
    )
  })

  it('shows requested beside reported, including repeats and every dual-stack address', async () => {
    await render(
      <SecondaryNetworksSection pod={pod({
        [MULTUS_NETWORKS_ANNOTATION]: 'macvlan-conf,macvlan-conf,pending-net',
        [MULTUS_NETWORK_STATUS_ANNOTATION]: status([
          { name: 'cbr0', interface: 'eth0', ips: ['10.244.1.73'], default: true },
          { name: 'apps/macvlan-conf', interface: 'net1', ips: ['192.168.1.205', '2001:db8::5'], mac: '86:1d:96:ff:55:0d' },
          { name: 'apps/macvlan-conf', interface: 'net2', ips: ['192.168.1.206'] },
        ]),
      })} />,
      container => {
        const text = container.textContent ?? ''
        expect(buttons(container).find(button => button.textContent?.startsWith('Secondary Networks'))?.getAttribute('aria-expanded')).toBe('true')
        expect(text).toContain('Secondary Networks (3)')
        expect(text).toContain('Default network')
        expect(text).toContain('net1')
        expect(text).toContain('net2')
        expect(text).toContain('192.168.1.205')
        expect(text).toContain('2001:db8::5')
        expect(text).toContain('86:1d:96:ff:55:0d')
        expect(text).toContain('Requested only')
        expect(text).toContain('Multus has not reported an interface for this request.')
        expect(text).toContain('Neither shows that the Pod can reach anything on a network.')
      },
    )
  })

  it('says so when the status annotation is absent', async () => {
    await render(
      <SecondaryNetworksSection pod={pod({ [MULTUS_NETWORKS_ANNOTATION]: 'a' })} />,
      container => {
        expect(container.textContent).toContain('Multus has not reported network status for this Pod.')
      },
    )
  })

  it('links a definition only once it is known to exist, with its exact API group', async () => {
    const onNavigate = vi.fn()
    const lookups: NetworkAttachmentLookups = { 'apps/macvlan-conf': 'found', 'shared/vlan-100': 'found' }
    await render(
      <SecondaryNetworksSection
        pod={pod({ [MULTUS_NETWORKS_ANNOTATION]: 'macvlan-conf,shared/vlan-100' })}
        attachmentLookups={lookups}
        onNavigate={onNavigate}
      />,
      async container => {
        const link = buttons(container).find(button => button.textContent === 'shared/vlan-100')
        expect(link).toBeDefined()
        await act(async () => link!.click())
        expect(onNavigate).toHaveBeenLastCalledWith({
          kind: 'network-attachment-definitions',
          group: 'k8s.cni.cncf.io',
          namespace: 'shared',
          name: 'vlan-100',
        })
        expect(buttons(container).some(button => button.textContent === 'macvlan-conf')).toBe(true)
      },
    )
  })

  it('keeps a definition unlinked and explains why when it is missing, forbidden, unchecked or unavailable', async () => {
    await render(
      <SecondaryNetworksSection
        pod={pod({ [MULTUS_NETWORKS_ANNOTATION]: 'gone,hidden,unknown,later' })}
        attachmentLookups={{ 'apps/gone': 'missing', 'apps/hidden': 'forbidden', 'apps/unknown': 'unavailable', 'apps/later': 'unchecked' }}
        onNavigate={() => undefined}
      />,
      container => {
        expect(buttons(container).filter(button => ['gone', 'hidden', 'unknown', 'later'].includes(button.textContent ?? ''))).toHaveLength(0)
        const text = container.textContent ?? ''
        expect(text).toContain('No NetworkAttachmentDefinition with this name in namespace apps.')
        expect(text).toContain('You don’t have permission to read this NetworkAttachmentDefinition.')
        expect(text).toContain('Could not check whether this NetworkAttachmentDefinition exists.')
        expect(text).toContain('Not checked: this Pod names more definitions than Radar looks up at once.')
      },
    )
  })

  it('makes no claim about a definition whose lookup is pending or not wired', async () => {
    await render(
      <SecondaryNetworksSection pod={pod({ [MULTUS_NETWORKS_ANNOTATION]: 'pending' })} attachmentLookups={{}} onNavigate={() => undefined} />,
      container => {
        expect(buttons(container).some(button => button.textContent === 'pending')).toBe(false)
        expect(container.textContent).not.toContain('NetworkAttachmentDefinition')
      },
    )
  })

  it('shows an unreadable annotation with its reason and raw value instead of no networks', async () => {
    await render(
      <SecondaryNetworksSection pod={pod({
        [MULTUS_NETWORKS_ANNOTATION]: '[{"name":"a"',
        [MULTUS_NETWORK_STATUS_ANNOTATION]: '{}',
      })} />,
      container => {
        const text = container.textContent ?? ''
        expect(text).toContain(`Could not read ${MULTUS_NETWORKS_ANNOTATION}`)
        expect(text).toContain('not valid JSON')
        expect(text).toContain('[{"name":"a"')
        expect(text).toContain(`Could not read ${MULTUS_NETWORK_STATUS_ANNOTATION}`)
        expect(text).toContain('expected a JSON list')
      },
    )
  })

  it('does not render a name that cannot identify a definition as a link', async () => {
    await render(
      <SecondaryNetworksSection
        pod={pod({ [MULTUS_NETWORKS_ANNOTATION]: JSON.stringify([{ name: '../secrets' }]) })}
        attachmentLookups={{ 'apps/../secrets': 'found' }}
        onNavigate={() => undefined}
      />,
      container => {
        expect(container.textContent).toContain('../secrets')
        expect(buttons(container).some(button => button.textContent === '../secrets')).toBe(false)
      },
    )
  })
})
