import { ConfirmDialog } from '@skyhook-io/k8s-ui'

const noop = () => {}

export function UninstallDanger() {
  return (
    <ConfirmDialog
      open
      onClose={noop}
      onConfirm={noop}
      title="Uninstall Release"
      message={'Are you sure you want to uninstall "ingress-nginx"?'}
      details={'This will remove the Helm release and all associated Kubernetes resources from the "ingress-nginx" namespace. This action cannot be undone.'}
      confirmLabel="Uninstall"
      variant="danger"
    />
  )
}

export function RollbackWarning() {
  return (
    <ConfirmDialog
      open
      onClose={noop}
      onConfirm={noop}
      title="Rollback Release"
      message={'Rollback "cert-manager" to revision 7?'}
      details="This will create a new revision that reverts the release to the state it was in at revision 7. The rollback will be applied to your cluster immediately."
      confirmLabel="Rollback"
      variant="warning"
    />
  )
}

export function WithCustomContent() {
  return (
    <ConfirmDialog
      open
      onClose={noop}
      onConfirm={noop}
      title="Drain Node"
      message="Evict all pods from ip-10-0-42-17.ec2.internal?"
      confirmLabel="Drain"
      variant="warning"
    >
      <div className="space-y-2 text-sm text-theme-text-secondary">
        <label className="flex items-center gap-2">
          <input type="checkbox" defaultChecked /> Ignore DaemonSet-managed pods
        </label>
        <label className="flex items-center gap-2">
          <input type="checkbox" /> Delete emptyDir data
        </label>
      </div>
    </ConfirmDialog>
  )
}

export function InProgress() {
  return (
    <ConfirmDialog
      open
      onClose={noop}
      onConfirm={noop}
      title="Delete Deployment"
      message={'Deleting "checkout" in namespace "payments"…'}
      confirmLabel="Delete"
      variant="danger"
      isLoading
    />
  )
}
