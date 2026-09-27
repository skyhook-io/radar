import { ServicePortCards, CollapseChevron } from '@skyhook-io/k8s-ui'
import { Plug } from 'lucide-react'

const wrap = { width: 460 }
const chip = 'inline-flex items-center gap-1 px-1.5 py-0.5 bg-theme-elevated rounded text-xs'

const checkoutApi = {
  metadata: { name: 'checkout-api', namespace: 'payments' },
  spec: {
    type: 'ClusterIP',
    clusterIP: '10.96.14.2',
    selector: { app: 'checkout-api' },
    ports: [
      { name: 'http', port: 80, targetPort: 8080, protocol: 'TCP', appProtocol: 'http' },
      { name: 'grpc', port: 9090, targetPort: 9090, protocol: 'TCP', appProtocol: 'grpc' },
      { name: 'metrics', port: 9102, targetPort: 'metrics', protocol: 'TCP' },
    ],
  },
}

const ingressNginx = {
  metadata: { name: 'ingress-nginx-controller', namespace: 'ingress-nginx' },
  spec: {
    type: 'NodePort',
    ports: [
      { name: 'http', port: 80, targetPort: 'http', nodePort: 30080, protocol: 'TCP' },
      { name: 'https', port: 443, targetPort: 'https', nodePort: 30443, protocol: 'TCP' },
    ],
  },
}

const coreDns = {
  metadata: { name: 'kube-dns', namespace: 'kube-system' },
  spec: {
    type: 'ClusterIP',
    ports: [
      { name: 'dns', port: 53, targetPort: 53, protocol: 'UDP' },
      { name: 'dns-tcp', port: 53, targetPort: 53, protocol: 'TCP' },
    ],
  },
}

export function Plain() {
  return (
    <div style={wrap}>
      <ServicePortCards service={checkoutApi} />
    </div>
  )
}

export function WithActions() {
  return (
    <div style={wrap}>
      <ServicePortCards
        service={checkoutApi}
        renderPortAction={({ port, protocol, appProtocol }) => (
          <>
            {appProtocol === 'http' && (
              <button type="button" className={`${chip} hover:bg-accent-muted`}>
                Curl <CollapseChevron open={false} className="w-3 h-3" />
              </button>
            )}
            <button type="button" className={`${chip} hover:bg-accent-muted`}>
              {port}/{protocol} <Plug className="w-3 h-3" />
            </button>
          </>
        )}
      />
    </div>
  )
}

export function NodePort() {
  return (
    <div style={wrap}>
      <ServicePortCards service={ingressNginx} />
    </div>
  )
}

export function UdpNotForwardable() {
  return (
    <div style={wrap}>
      <ServicePortCards
        service={coreDns}
        renderPortAction={({ port, protocol }) =>
          protocol === 'UDP' ? (
            <span className={`${chip} text-theme-text-tertiary opacity-60 cursor-default`}>
              {port}/{protocol} <Plug className="w-3 h-3" />
            </span>
          ) : (
            <button type="button" className={`${chip} hover:bg-accent-muted`}>
              {port}/{protocol} <Plug className="w-3 h-3" />
            </button>
          )
        }
      />
    </div>
  )
}
