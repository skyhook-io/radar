import { AlertBanner } from '@skyhook-io/k8s-ui'
import { ShieldAlert } from 'lucide-react'

const wrap = { width: 480 }

export function Variants() {
  return (
    <div style={wrap}>
      <AlertBanner
        variant="error"
        title="Gateway Not Accepted"
        message={<><span className="font-medium">InvalidParameters: </span>GatewayClass "istio" does not support listener protocol UDP.</>}
      />
      <AlertBanner
        variant="warning"
        title="Gateway Not Programmed"
        message={<><span className="font-medium">Pending: </span>Waiting for the load balancer address to be assigned.</>}
      />
      <AlertBanner variant="info" title="Suspended" message="Reconciliation is paused; changes in Git will not be applied until resumed." />
      <AlertBanner variant="success" title="Workflow Completed Successfully" />
    </div>
  )
}

export function WithItems() {
  return (
    <div style={wrap}>
      <AlertBanner
        variant="error"
        title="Backup Failed"
        items={[
          'Invalid included/excluded namespace lists: namespace "paymnts" not found',
          'BackupStorageLocation "aws-us-east-1" is unavailable',
        ]}
      />
    </div>
  )
}

export function CustomIcon() {
  return (
    <div style={wrap}>
      <AlertBanner
        variant="warning"
        icon={ShieldAlert}
        title="Policy violations in payments"
        message="3 resources fail require-requests-limits. Admission is set to Audit, so they were not blocked."
      />
    </div>
  )
}
