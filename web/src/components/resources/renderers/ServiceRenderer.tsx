import { useMemo, useState, useCallback } from 'react'
import { ServiceRenderer as BaseServiceRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/ServiceRenderer'
import { PortForwardInlineButton } from '../../portforward/PortForwardButton'
import { CurlButton, CurlPanel, isHttpishPort, defaultScheme, defaultPathForPort } from '../../curl/ServiceCurlButton'
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
  // Which port's inline curl panel is open (one at a time). `closing` keeps the
  // panel mounted through its collapse animation before we drop it.
  const [curl, setCurl] = useState<{ port: number; closing: boolean } | null>(null)
  const closeCurl = useCallback(() => {
    setCurl((p) => (p ? { ...p, closing: true } : null))
    // Only drop the panel if it's still the one closing. Opening another port
    // (which sets closing:false) before this fires must not clear the new panel.
    window.setTimeout(() => setCurl((p) => (p?.closing ? null : p)), 220)
  }, [])
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
            <CurlButton
              active={curl?.port === port && !curl.closing}
              onClick={() => {
                if (curl?.port === port && !curl.closing) closeCurl()
                else setCurl({ port, closing: false })
              }}
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
        curl?.port === port ? (
          <CurlPanel
            namespace={namespace}
            serviceName={serviceName}
            port={port}
            initialScheme={defaultScheme(port, name, appProtocol)}
            initialPath={defaultPathForPort(port, name, appProtocol)}
            open={!curl.closing}
            onClose={closeCurl}
          />
        ) : null
      }
    />
  )
}
