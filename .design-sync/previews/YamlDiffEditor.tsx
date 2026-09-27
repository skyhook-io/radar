import { YamlDiffEditor } from '@skyhook-io/k8s-ui'

const frame = { width: 720, maxWidth: '100%', height: 320 }

const original = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: checkout-api
  namespace: production
spec:
  replicas: 3
  template:
    spec:
      containers:
        - name: checkout-api
          image: registry.skyhook.io/checkout-api:v2.4.0
          resources:
            limits:
              cpu: "1"
              memory: 512Mi
`

const modified = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: checkout-api
  namespace: production
spec:
  replicas: 5
  template:
    spec:
      containers:
        - name: checkout-api
          image: registry.skyhook.io/checkout-api:v2.4.1
          resources:
            limits:
              cpu: "2"
              memory: 1Gi
`

export function SideBySide() {
  return (
    <div style={frame}>
      <YamlDiffEditor original={original} modified={modified} height="100%" />
    </div>
  )
}

export function Unified() {
  return (
    <div style={frame}>
      <YamlDiffEditor original={original} modified={modified} unified height="100%" />
    </div>
  )
}
