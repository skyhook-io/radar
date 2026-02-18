# CRD Integrations

Radar automatically discovers and displays any Custom Resource Definition (CRD) in your cluster. For popular tools, Radar goes further — providing dedicated detail views, topology edges, smart table columns, and AI-optimized summaries.

## Karpenter

[Karpenter](https://karpenter.sh/) is the standard node autoscaler for Kubernetes, replacing Cluster Autoscaler on AWS (EKS), Azure (AKS NAP), and generic clusters.

### What Radar Shows

**Topology:** Full provisioning chain — NodePool → NodeClaim → Node → Pod. See which NodePool owns which NodeClaims, which Nodes they provisioned, and what Pods are running on them.

<p align="center">
  <img src="screenshots/integrations/karpenter-topology.png" alt="Karpenter Topology" width="800">
  <br><em>Karpenter in Topology View — NodePool → NodeClaim provisioning chain</em>
</p>

**NodePool Detail View:**
- Status conditions (Ready)
- NodeClass reference (EC2NodeClass, AKSNodeClass, or generic)
- Resource limits (CPU, memory)
- Disruption policy and consolidation settings
- Instance requirements (types, zones, architectures)

<p align="center">
  <img src="screenshots/integrations/karpenter-nodepool-detail.png" alt="NodePool Detail" width="800">
  <br><em>NodePool Detail View — Status, related NodeClaims, and full specification</em>
</p>

**NodeClaim Detail View:**
- Status conditions (Initialized, Launched, Registered, Ready)
- Instance type and capacity
- Node name and NodePool reference
- Provisioning timeline

<!-- NodeClaim detail screenshot pending -->

**Resource Browser:** Smart columns show status, NodeClass reference, limits, and disruption policy at a glance.

<p align="center">
  <img src="screenshots/integrations/karpenter-nodepools-list.png" alt="NodePool List" width="800">
  <br><em>NodePool Resource Browser — Status, NodeClass, limits, and disruption policy at a glance</em>
</p>

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| NodePool | `karpenter.sh/v1` | Yes | Yes | Yes |
| NodeClaim | `karpenter.sh/v1` | Yes | Yes | Yes |

Provider-specific NodeClasses (EC2NodeClass, AKSNodeClass, etc.) are auto-discovered and browsable via the generic CRD viewer.

---

## KEDA

[KEDA](https://keda.sh/) (Kubernetes Event-Driven Autoscaling) is a CNCF graduated project that scales workloads based on external event sources — queues, streams, cron schedules, Prometheus metrics, and 60+ other triggers.

### What Radar Shows

**Topology:** ScaledObject → target workload (Deployment, StatefulSet, or Rollout). See which workloads are managed by KEDA and trace the scaling relationship.

<p align="center">
  <img src="screenshots/integrations/keda-topology.png" alt="KEDA Topology" width="800">
  <br><em>KEDA in Topology View — ScaledObject → Deployment → Pod scaling chain</em>
</p>

**ScaledObject Detail View:**
- Status conditions (Ready, Active, Paused, Fallback)
- Target workload reference
- Min/Max/Idle replica configuration
- Polling interval and cooldown period
- Trigger list with type and metadata
- Generated HPA name
- Pause state detection (supports all 3 annotation variants)

<p align="center">
  <img src="screenshots/integrations/keda-scaledobject-detail.png" alt="ScaledObject Detail" width="800">
  <br><em>ScaledObject Detail View — Status conditions, target workload, triggers, and replica configuration</em>
</p>

**ScaledJob Detail View:**
- Status conditions
- Job target reference
- Scaling strategy (default, custom, accurate, eager)
- Success/failure limits
- Trigger list

<!-- ScaledJob detail screenshot pending -->

**Resource Browser:** Smart columns show status, target workload, trigger count, and replica range at a glance.

<p align="center">
  <img src="screenshots/integrations/keda-scaledobjects-list.png" alt="ScaledObject List" width="800">
  <br><em>ScaledObject Resource Browser — Status, target workload, trigger count, and replica range</em>
</p>

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| ScaledObject | `keda.sh/v1alpha1` | Yes | Yes | Yes |
| ScaledJob | `keda.sh/v1alpha1` | Yes | Yes | Yes |
| TriggerAuthentication | `keda.sh/v1alpha1` | — | Generic | — |
| ClusterTriggerAuthentication | `keda.sh/v1alpha1` | — | Generic | — |

---

## Gateway API

[Gateway API](https://gateway-api.sigs.k8s.io/) is the next-generation Kubernetes networking API, replacing Ingress with more expressive routing, traffic splitting, and multi-tenant support.

### What Radar Shows

**Topology:** Full network path — GatewayClass → Gateway → HTTPRoute/GRPCRoute/TCPRoute/TLSRoute → Service → Pod. Visualize how traffic flows from the gateway controller through routes to your backend services.

<p align="center">
  <img src="screenshots/integrations/gateway-topology.png" alt="Gateway API Topology" width="800">
  <br><em>Gateway API in Topology View — GatewayClass → Gateway → HTTPRoute → Service traffic path</em>
</p>

**Gateway Detail View:** Listeners, addresses, attached routes, and status conditions.

**HTTPRoute Detail View:** Rules with path/header matching, backend references, filters, and weights.

**GatewayClass:** Appears as a cluster-scoped parent node in topology, showing which controller manages your Gateways.

<!-- Gateway detail screenshot pending -->

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| GatewayClass | `gateway.networking.k8s.io/v1` | Yes | Generic | Yes |
| Gateway | `gateway.networking.k8s.io/v1` | Yes | Yes | — |
| HTTPRoute | `gateway.networking.k8s.io/v1` | Yes | Yes | — |
| GRPCRoute | `gateway.networking.k8s.io/v1` | Yes | Generic | — |
| TCPRoute | `gateway.networking.k8s.io/v1alpha2` | Yes | Generic | — |
| TLSRoute | `gateway.networking.k8s.io/v1alpha2` | Yes | Generic | — |

---

## cert-manager

[cert-manager](https://cert-manager.io/) automates TLS certificate management — issuing, renewing, and revoking certificates from Let's Encrypt, Vault, Venafi, and other issuers.

### What Radar Shows

**Topology:** Certificate → Issuer/ClusterIssuer edges show which issuer manages each certificate. The full provisioning chain (Certificate → CertificateRequest → Order → Challenge) is connected via owner references.

<p align="center">
  <img src="screenshots/integrations/certmanager-topology.png" alt="cert-manager Topology" width="800">
  <br><em>cert-manager in Topology View — Certificate → CertificateRequest provisioning chain</em>
</p>

**Certificate Detail View:**
- Status conditions (Ready) with color-coded expiry warnings
- Validity period with progress bar (green → yellow → red as expiry approaches)
- Subject, DNS names, issuer reference
- Renewal time and last failure

**Dashboard:** Certificate health card showing healthy/warning/critical/expired certificate counts across all namespaces.

**TLS Secret Parsing:** Click any TLS Secret to see the X.509 certificate details — subject, issuer, validity dates, SANs — parsed directly from the secret data.

<p align="center">
  <img src="screenshots/integrations/certmanager-certificate-detail.png" alt="Certificate Detail" width="800">
  <br><em>Certificate Detail View — Validity progress bar, DNS names, issuer reference, and status conditions</em>
</p>

<p align="center">
  <img src="screenshots/integrations/certmanager-certificates-list.png" alt="Certificate List" width="800">
  <br><em>Certificate Resource Browser — Ready status, domains, issuer, and expiry date at a glance</em>
</p>

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| Certificate | `cert-manager.io/v1` | Yes | Yes | Yes |
| CertificateRequest | `cert-manager.io/v1` | Yes | Yes | — |
| Issuer | `cert-manager.io/v1` | Yes | Yes | — |
| ClusterIssuer | `cert-manager.io/v1` | Yes | Yes | — |
| Order | `acme.cert-manager.io/v1` | Yes | Yes | — |
| Challenge | `acme.cert-manager.io/v1` | Yes | Yes | — |

---

## Trivy Operator

[Trivy Operator](https://aquasecurity.github.io/trivy-operator/) continuously scans your cluster for vulnerabilities, misconfigurations, exposed secrets, and license compliance issues.

### What Radar Shows

**VulnerabilityReport Detail View:** Severity breakdown (Critical/High/Medium/Low), affected images, and CVE counts.

**ConfigAuditReport Detail View:** Pass/fail checks with severity levels.

**Resource Browser:** Smart columns show severity counts and scan status at a glance.

<!-- VulnerabilityReport detail screenshot pending -->

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| VulnerabilityReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |
| ConfigAuditReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |
| ExposedSecretReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |
| ClusterComplianceReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |
| SbomReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |

---

## GitOps

See the main [README](../README.md#gitops) for GitOps integration details.

### FluxCD

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| GitRepository | `source.toolkit.fluxcd.io/v1` | Yes | Yes | Yes |
| OCIRepository | `source.toolkit.fluxcd.io/v1beta2` | Yes | Yes | — |
| HelmRepository | `source.toolkit.fluxcd.io/v1` | Yes | Yes | — |
| Kustomization | `kustomize.toolkit.fluxcd.io/v1` | Yes | Yes | Yes |
| HelmRelease | `helm.toolkit.fluxcd.io/v2` | Yes | Yes | Yes |
| Alert | `notification.toolkit.fluxcd.io/v1beta3` | — | Yes | — |

### ArgoCD

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| Application | `argoproj.io/v1alpha1` | Yes | Yes | Yes |
| ApplicationSet | `argoproj.io/v1alpha1` | — | Generic | — |
| AppProject | `argoproj.io/v1alpha1` | — | Generic | — |

---

## Other CRDs

Any CRD installed in your cluster is automatically discovered and browsable. Resources appear in the sidebar, can be filtered and searched, and show full YAML in the detail drawer. The generic viewer works with any resource — the integrations above simply add richer presentation.

### Argo Rollouts

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| Rollout | `argoproj.io/v1alpha1` | Yes | Yes | Yes |

### Argo Workflows

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| Workflow | `argoproj.io/v1alpha1` | — | Yes | — |
| WorkflowTemplate | `argoproj.io/v1alpha1` | — | Yes | — |
