// Skeleton YAML templates for common Kubernetes resource kinds.
// Used by the Create Resource dialog to pre-fill the editor.

const skeletons: Record<string, string> = {
  Pod: `apiVersion: v1
kind: Pod
metadata:
  name: my-pod
  namespace: default
spec:
  containers:
    - name: app
      image: nginx:latest
      ports:
        - containerPort: 80`,

  Deployment: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deployment
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: my-deployment
  template:
    metadata:
      labels:
        app: my-deployment
    spec:
      containers:
        - name: app
          image: nginx:latest
          ports:
            - containerPort: 80`,

  Service: `apiVersion: v1
kind: Service
metadata:
  name: my-service
  namespace: default
spec:
  selector:
    app: my-app
  ports:
    - port: 80
      targetPort: 80
  type: ClusterIP`,

  EndpointSlice: `apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: my-service-1
  namespace: default
  labels:
    kubernetes.io/service-name: my-service
addressType: IPv4
ports:
  - name: http
    protocol: TCP
    port: 80
endpoints:
  - addresses:
      - 10.0.0.10
    conditions:
      ready: true
      serving: true`,

  ConfigMap: `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-configmap
  namespace: default
data:
  key: value`,

  Secret: `apiVersion: v1
kind: Secret
metadata:
  name: my-secret
  namespace: default
type: Opaque
stringData:
  key: value`,

  Namespace: `apiVersion: v1
kind: Namespace
metadata:
  name: my-namespace`,

  Job: `apiVersion: batch/v1
kind: Job
metadata:
  name: my-job
  namespace: default
spec:
  template:
    spec:
      containers:
        - name: job
          image: busybox:latest
          command: ["echo", "hello"]
      restartPolicy: Never
  backoffLimit: 3`,

  CronJob: `apiVersion: batch/v1
kind: CronJob
metadata:
  name: my-cronjob
  namespace: default
spec:
  schedule: "*/5 * * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: job
              image: busybox:latest
              command: ["echo", "hello"]
          restartPolicy: Never`,

  Ingress: `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-ingress
  namespace: default
spec:
  rules:
    - host: example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: my-service
                port:
                  number: 80`,

  PersistentVolumeClaim: `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-pvc
  namespace: default
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi`,

  ServiceAccount: `apiVersion: v1
kind: ServiceAccount
metadata:
  name: my-service-account
  namespace: default`,

  StatefulSet: `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: my-statefulset
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: my-statefulset
  serviceName: my-statefulset
  template:
    metadata:
      labels:
        app: my-statefulset
    spec:
      containers:
        - name: app
          image: nginx:latest
          ports:
            - containerPort: 80`,

  DaemonSet: `apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: my-daemonset
  namespace: default
spec:
  selector:
    matchLabels:
      app: my-daemonset
  template:
    metadata:
      labels:
        app: my-daemonset
    spec:
      containers:
        - name: app
          image: nginx:latest`,
}

// Returns skeleton YAML for a known kind, or a generic template for unknown kinds.
export function getSkeletonYaml(kind: string, group?: string): string {
  const skeleton = skeletons[kind]
  if (skeleton) return skeleton

  // For unknown/CRD kinds, provide a minimal template
  let apiVersion = 'v1'
  if (group) {
    apiVersion = `${group}/v1`
  }

  return `apiVersion: ${apiVersion}
kind: ${kind}
metadata:
  name: my-${kind.toLowerCase()}
  namespace: default`
}

// Strips cluster-managed metadata from a resource YAML for duplication.
export function cleanYamlForDuplicate(yamlContent: string): string {
  const lines = yamlContent.split('\n')
  const result: string[] = []
  let skipUntilDedent = false
  let skipIndent = 0

  const stripTopLevelKeys = new Set([
    'status:',
  ])

  const stripMetadataKeys = new Set([
    'resourceVersion:',
    'uid:',
    'creationTimestamp:',
    'generation:',
    'managedFields:',
    'selfLink:',
  ])

  const stripAnnotationKeys = [
    'kubectl.kubernetes.io/last-applied-configuration:',
  ]

  let inMetadata = false
  let inAnnotations = false
  let metadataIndent = 0
  let annotationsIndent = 0

  for (const line of lines) {
    const trimmed = line.trimStart()
    const indent = line.length - trimmed.length

    // Handle skip mode (for multi-line values like managedFields or status)
    if (skipUntilDedent) {
      if (indent > skipIndent || trimmed === '' || trimmed.startsWith('-')) {
        continue
      }
      skipUntilDedent = false
    }

    // Track metadata section
    if (trimmed === 'metadata:') {
      inMetadata = true
      metadataIndent = indent
      result.push(line)
      continue
    }

    if (inMetadata && indent <= metadataIndent && trimmed !== '' && trimmed !== 'metadata:') {
      inMetadata = false
      inAnnotations = false
    }

    // Track annotations within metadata
    if (inMetadata && trimmed === 'annotations:') {
      inAnnotations = true
      annotationsIndent = indent
      result.push(line)
      continue
    }

    if (inAnnotations && indent <= annotationsIndent && trimmed !== '' && trimmed !== 'annotations:') {
      inAnnotations = false
    }

    // Strip top-level keys (status)
    if (indent === 0 && stripTopLevelKeys.has(trimmed.split(' ')[0])) {
      skipUntilDedent = true
      skipIndent = indent
      continue
    }

    // Strip metadata keys
    if (inMetadata && !inAnnotations) {
      const key = trimmed.split(' ')[0]
      if (stripMetadataKeys.has(key)) {
        if (key === 'managedFields:') {
          skipUntilDedent = true
          skipIndent = indent
        }
        continue
      }
      // Clear the name value (user needs to provide a new one)
      if (trimmed.startsWith('name:')) {
        result.push(line.replace(/name:.*/, 'name: '))
        continue
      }
    }

    // Strip specific annotations
    if (inAnnotations) {
      const shouldStrip = stripAnnotationKeys.some(key => trimmed.startsWith(key))
      if (shouldStrip) {
        continue
      }
    }

    result.push(line)
  }

  return result.join('\n')
}
