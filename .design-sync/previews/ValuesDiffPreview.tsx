import { ValuesDiffPreview } from '@skyhook-io/k8s-ui'

const noop = () => {}

const manifestDiff = `--- a/checkout-api/templates/deployment.yaml
+++ b/checkout-api/templates/deployment.yaml
@@ -8,7 +8,7 @@ metadata:
   name: checkout-api
 spec:
-  replicas: 3
+  replicas: 5
   selector:
     matchLabels:
       app.kubernetes.io/name: checkout-api
@@ -31,10 +31,10 @@ spec:
         - name: checkout-api
-          image: ghcr.io/acme/checkout-api:1.18.3
+          image: ghcr.io/acme/checkout-api:1.19.0
           resources:
             requests:
-              cpu: 250m
-              memory: 256Mi
+              cpu: 500m
+              memory: 512Mi
             limits:
               memory: 1Gi`

export function ValuesChange() {
  return (
    <ValuesDiffPreview
      previewData={{
        currentValues: { replicaCount: 3, image: { tag: '1.18.3' } },
        newValues: { replicaCount: 5, image: { tag: '1.19.0' } },
        manifestDiff,
      }}
      onClose={noop}
      onApply={noop}
      isApplying={false}
    />
  )
}

export function ChartUpgradeApplying() {
  return (
    <ValuesDiffPreview
      previewData={{
        currentValues: {},
        newValues: {},
        manifestDiff: `--- a/ingress-nginx/templates/controller-deployment.yaml
+++ b/ingress-nginx/templates/controller-deployment.yaml
@@ -12,7 +12,7 @@ metadata:
-    helm.sh/chart: ingress-nginx-4.11.3
+    helm.sh/chart: ingress-nginx-4.12.0
-    app.kubernetes.io/version: "1.11.3"
+    app.kubernetes.io/version: "1.12.0"
@@ -58,4 +58,4 @@ spec:
-          image: registry.k8s.io/ingress-nginx/controller:v1.11.3
+          image: registry.k8s.io/ingress-nginx/controller:v1.12.0`,
      }}
      onClose={noop}
      onApply={noop}
      isApplying
      title="Preview upgrade to 4.12.0"
      applyLabel="Upgrade"
    />
  )
}

export function NoManifestChanges() {
  return (
    <ValuesDiffPreview
      previewData={{
        currentValues: { podAnnotations: {} },
        newValues: { podAnnotations: { 'team': 'payments' } },
        manifestDiff: '',
      }}
      onClose={noop}
      onApply={noop}
      isApplying={false}
    />
  )
}
