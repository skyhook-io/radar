import { YamlReview, type YamlPreviewResult } from '@skyhook-io/k8s-ui'

const noop = () => {}
const frame = {
  width: 640,
  height: 580,
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  borderRadius: 8,
} as const

const liveDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: checkout-api
  namespace: payments
spec:
  replicas: 3
  selector:
    matchLabels:
      app: checkout-api
  template:
    metadata:
      labels:
        app: checkout-api
    spec:
      containers:
        - name: checkout-api
          image: ghcr.io/acme/checkout-api:v2.14.0
          ports:
            - containerPort: 8080
          resources:
            limits:
              memory: 256Mi
`

const editedDeployment = liveDeployment
  .replace('replicas: 3', 'replicas: 5')
  .replace('checkout-api:v2.14.0', 'checkout-api:v2.15.1')
  .replace('memory: 256Mi', 'memory: 512Mi')

export function SingleUpdate() {
  const documents: YamlPreviewResult[] = [
    {
      index: 0,
      status: 'accepted',
      apiVersion: 'apps/v1',
      kind: 'Deployment',
      namespace: 'payments',
      name: 'checkout-api',
      action: 'update',
      baselineYaml: liveDeployment,
      predictedYaml: editedDeployment,
      reviewedResourceVersion: '48213377',
    },
  ]
  return (
    <div style={frame} className="bg-theme-surface border border-theme-border">
      <YamlReview
        submittedYaml={editedDeployment}
        originalYaml={liveDeployment}
        documents={documents}
        onBack={noop}
        onApply={noop}
      />
    </div>
  )
}

const configMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: checkout-flags
  namespace: payments
data:
  ENABLE_APPLE_PAY: "true"
`
const service = `apiVersion: v1
kind: Service
metadata:
  name: checkout-api
  namespace: payments
spec:
  selector:
    app: checkout-api
  ports:
    - port: 80
      targetPort: 8080
`
const monitor = `apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: checkout-api
  namespace: payments
spec:
  endpoints:
    - port: metrics
`

export function MultiDocumentMixed() {
  const documents: YamlPreviewResult[] = [
    { index: 0, status: 'accepted', apiVersion: 'v1', kind: 'ConfigMap', namespace: 'payments', name: 'checkout-flags', action: 'create', submittedYaml: configMap, predictedYaml: configMap },
    {
      index: 1,
      status: 'accepted',
      apiVersion: 'v1',
      kind: 'Service',
      namespace: 'payments',
      name: 'checkout-api',
      action: 'create',
      submittedYaml: service,
      predictedYaml: service,
      warnings: ['spec.ports[0].name is empty; named ports are required for Istio protocol selection'],
    },
    {
      index: 2,
      status: 'unavailable',
      apiVersion: 'monitoring.coreos.com/v1',
      kind: 'ServiceMonitor',
      namespace: 'payments',
      name: 'checkout-api',
      action: 'unknown',
      submittedYaml: monitor,
      error: 'no matches for kind "ServiceMonitor" in version "monitoring.coreos.com/v1"; ensure CRDs are installed first',
    },
  ]
  return (
    <div style={frame} className="bg-theme-surface border border-theme-border">
      <YamlReview
        submittedYaml={[configMap, service, monitor].join('---\n')}
        documents={documents}
        nonAtomic
        applyLabel="Create reviewed resources"
        onClose={noop}
        onBack={noop}
        onApply={noop}
      />
    </div>
  )
}

export function Rejected() {
  const bad = editedDeployment.replace('replicas: 5', 'replicas: -1')
  const documents: YamlPreviewResult[] = [
    {
      index: 0,
      status: 'rejected',
      apiVersion: 'apps/v1',
      kind: 'Deployment',
      namespace: 'payments',
      name: 'checkout-api',
      action: 'update',
      baselineYaml: liveDeployment,
      submittedYaml: bad,
      error: 'Deployment.apps "checkout-api" is invalid: spec.replicas: Invalid value: -1: must be greater than or equal to 0',
    },
  ]
  return (
    <div style={frame} className="bg-theme-surface border border-theme-border">
      <YamlReview submittedYaml={bad} originalYaml={liveDeployment} documents={documents} onBack={noop} onApply={noop} />
    </div>
  )
}
