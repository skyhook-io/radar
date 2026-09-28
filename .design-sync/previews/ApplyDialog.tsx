import { ApplyDialog } from '@skyhook-io/k8s-ui'

const noop = () => {}

const fix = 'Raise the memory limit on the `checkout-api` container from 256Mi to 512Mi:\n\n```\nkubectl -n payments set resources deployment/checkout-api -c checkout-api --limits=memory=512Mi\n```'

export function RecommendedFix() {
  return (
    <ApplyDialog
      open
      onClose={noop}
      onConfirm={noop}
      agentLabel="Claude Code"
      resourceLabel="Deployment payments/checkout-api"
      context="gke_acme-prod_us-east1_prod-cluster-us-east1"
      fix={fix}
      reason="the container is OOMKilled at its 256Mi limit within a minute of each start; raising the limit stops the restarts without touching application code."
      precondition="the node pool has at least 512Mi allocatable per replica"
      confidence={0.86}
    />
  )
}

export function GitOpsManaged() {
  return (
    <ApplyDialog
      open
      onClose={noop}
      onConfirm={noop}
      agentLabel="Codex"
      resourceLabel="Deployment payments/checkout-api"
      context="kind-radar-gitops-demo"
      fix={'Point the image back to the last tag that pulled successfully:\n\n```\nkubectl -n payments set image deployment/checkout-api checkout-api=ghcr.io/acme/checkout-api:v2.14.1\n```'}
      managedBy="Argo CD"
      confidence={0.42}
    />
  )
}
