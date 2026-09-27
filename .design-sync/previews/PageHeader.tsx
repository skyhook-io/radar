import { Badge, PageHeader } from '@skyhook-io/k8s-ui'
import { Boxes, Server, ShieldAlert } from 'lucide-react'

const panel = { width: 620, maxWidth: '100%' }

export function Basic() {
  return (
    <div style={panel}>
      <PageHeader
        icon={Boxes}
        title="Workloads"
        description="Deployments, StatefulSets, DaemonSets and Jobs across all namespaces"
      />
    </div>
  )
}

export function WithActions() {
  return (
    <div style={panel}>
      <PageHeader
        icon={Server}
        title="Nodes"
        description="prod-cluster-us-east1"
        actions={
          <>
            <Badge severity="success">18 Ready</Badge>
            <Badge severity="warning">1 NotReady</Badge>
            <Badge kind="Node">6 GPU</Badge>
          </>
        }
      />
    </div>
  )
}

export function WithBackButton() {
  return (
    <div style={panel}>
      <PageHeader
        icon={ShieldAlert}
        title="checkout-api"
        description="Deployment · namespace payments"
        onBack={() => {}}
        actions={<Badge severity="alert">3 findings</Badge>}
      />
    </div>
  )
}
