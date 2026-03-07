import { ServiceRenderer as BaseServiceRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/ServiceRenderer'
import { PortForwardInlineButton } from '../../portforward/PortForwardButton'

interface ServiceRendererProps {
  data: any
  onCopy: (text: string, label: string) => void
  copied: string | null
}

export function ServiceRenderer({ data, onCopy, copied }: ServiceRendererProps) {
  return (
    <BaseServiceRenderer
      data={data}
      onCopy={onCopy}
      copied={copied}
      renderPortAction={({ namespace, serviceName, port, protocol }) => (
        <PortForwardInlineButton
          namespace={namespace}
          serviceName={serviceName}
          port={port}
          protocol={protocol}
        />
      )}
    />
  )
}
