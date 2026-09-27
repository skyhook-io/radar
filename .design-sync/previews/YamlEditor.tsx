import { YamlEditor } from '@skyhook-io/k8s-ui'

const noop = () => {}
const frame = { width: 640, maxWidth: '100%', height: 320 }

const deploymentYaml = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: checkout-api
  namespace: production
  labels:
    app: checkout-api
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
          image: registry.skyhook.io/checkout-api:v2.4.1
          ports:
            - containerPort: 8080
          resources:
            requests:
              cpu: 250m
              memory: 256Mi
            limits:
              cpu: "1"
              memory: 512Mi
`

const podYaml = `apiVersion: v1
kind: Pod
metadata:
  name: cart-service-7d9c4b8f6-x2kqz
  namespace: production
spec:
  containers:
    - name: cart-service
      image: registry.skyhook.io/cart-service:1.8.0
      ports:
        - containerPort: 9000
  tolerations:
    - key: node.kubernetes.io/not-ready
      operator: Exists
      effect: NoExecute
`

export function EditableDeployment() {
  return (
    <div style={frame}>
      <YamlEditor value={deploymentYaml} onChange={noop} height="100%" />
    </div>
  )
}

export function ReadOnlyPod() {
  return (
    <div style={frame}>
      <YamlEditor value={podYaml} readOnly kind="Pod" height="100%" />
    </div>
  )
}
