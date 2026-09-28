import { ManifestViewer } from '@skyhook-io/k8s-ui'

const frame = { width: 760 } as const
const noop = () => {}

const manifest = `---
# Source: checkout-api/templates/serviceaccount.yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: checkout-api
  labels:
    app.kubernetes.io/name: checkout-api
    app.kubernetes.io/instance: checkout-api
    helm.sh/chart: checkout-api-2.4.1
---
# Source: checkout-api/templates/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: checkout-api
spec:
  type: ClusterIP
  ports:
    - port: 8080
      targetPort: http
      name: http
  selector:
    app.kubernetes.io/name: checkout-api
---
# Source: checkout-api/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: checkout-api
spec:
  replicas: 3
  selector:
    matchLabels:
      app.kubernetes.io/name: checkout-api
  template:
    spec:
      serviceAccountName: checkout-api
      containers:
        - name: checkout-api
          image: ghcr.io/acme/checkout-api:1.18.3
          ports:
            - name: http
              containerPort: 8080
          resources:
            requests:
              cpu: 250m
              memory: 256Mi`

export function RenderedRevision() {
  return (
    <div style={frame}>
      <ManifestViewer manifest={manifest} isLoading={false} revision={14} onCopy={noop} copied={false} />
    </div>
  )
}

export function JustCopied() {
  return (
    <div style={frame}>
      <ManifestViewer
        manifest={manifest.split('\n').slice(0, 24).join('\n')}
        isLoading={false}
        revision={13}
        onCopy={noop}
        copied
      />
    </div>
  )
}

export function Loading() {
  return (
    <div style={frame}>
      <ManifestViewer manifest="" isLoading revision={14} onCopy={noop} copied={false} />
    </div>
  )
}

export function NoManifest() {
  return (
    <div style={frame}>
      <ManifestViewer manifest="" isLoading={false} onCopy={noop} copied={false} />
    </div>
  )
}
