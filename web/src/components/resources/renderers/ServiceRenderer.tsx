import { useMemo, useState } from 'react'
import { ServiceRenderer as BaseServiceRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/ServiceRenderer'
import { PortForwardInlineButton } from '../../portforward/PortForwardButton'
import { ProbeButton, ProbePanel, isHttpishPort, defaultScheme, defaultPathForPort } from '../../probe/ServiceProbeButton'
import { useResources } from '../../../api/client'
import { useNamespacedCapabilities, useIsLocalDeployment } from '../../../contexts/CapabilitiesContext'
import type { ResourceRef } from '../../../types'

interface ServiceRendererProps {
  data: any
  onCopy: (text: string, label: string) => void
  copied: string | null
  onNavigate?: (ref: ResourceRef) => void
}

export function ServiceRenderer({ data, onCopy, copied, onNavigate }: ServiceRendererProps) {
  const namespace = data.metadata?.namespace
  const serviceName = data.metadata?.name
  const { canPortForward } = useNamespacedCapabilities(namespace)
  const isLocal = useIsLocalDeployment()
  // Offer the port-forward affordance when a live forward is possible (local +
  // RBAC) OR when we're not local — in-cluster/Cloud can't bind a local listener,
  // but we still surface a copy-paste `kubectl port-forward` command.
  const showPortForward = canPortForward || !isLocal
  // Curl dials the Service directly from in-cluster, so it's only available when
  // Radar runs in-cluster/Cloud — locally you'd port-forward instead.
  const showCurl = !isLocal
  // Which port's inline probe panel is open (one at a time). Keyed by port number.
  const [activeProbePort, setActiveProbePort] = useState<number | null>(null)
  const spec = data.spec || {}
  const shouldLoadEndpointSlices = Boolean(
    namespace &&
    serviceName &&
    spec.type !== 'ExternalName' &&
    (!spec.selector || Object.keys(spec.selector).length === 0)
  )
  const { data: endpointSlices, isLoading: endpointSlicesLoading } = useResources<any>(
    'endpointslices',
    namespace,
    'discovery.k8s.io',
    { enabled: shouldLoadEndpointSlices, refetchInterval: 30000 }
  )
  const matchingEndpointSlices = useMemo(
    () => (endpointSlices || []).filter((slice: any) => slice.metadata?.labels?.['kubernetes.io/service-name'] === serviceName),
    [endpointSlices, serviceName]
  )

  return (
    <BaseServiceRenderer
      data={data}
      onCopy={onCopy}
      copied={copied}
      endpointSlices={matchingEndpointSlices}
      endpointSlicesLoading={endpointSlicesLoading}
      onNavigate={onNavigate}
      renderPortAction={({ port, name, appProtocol, protocol }) => (
        <>
          {showCurl && isHttpishPort(port, name, appProtocol, protocol) && (
            <ProbeButton
              active={activeProbePort === port}
              onClick={() => setActiveProbePort((cur) => (cur === port ? null : port))}
            />
          )}
          {showPortForward && (
            <PortForwardInlineButton
              namespace={namespace}
              serviceName={serviceName}
              port={port}
              protocol={protocol}
            />
          )}
        </>
      )}
      renderPortPanel={({ port, name, appProtocol }) =>
        activeProbePort === port ? (
          <ProbePanel
            namespace={namespace}
            serviceName={serviceName}
            port={port}
            initialScheme={defaultScheme(port, name, appProtocol)}
            initialPath={defaultPathForPort(port, name, appProtocol)}
            onClose={() => setActiveProbePort(null)}
          />
        ) : null
      }
    />
  )
}
