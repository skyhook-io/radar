# CRD Integrations

Radar automatically discovers and displays **any** Custom Resource Definition (CRD) in your cluster — no configuration needed. For popular tools, Radar provides dedicated detail views, topology edges, smart table columns, and AI-optimized summaries for seamless integration.

---

## Karpenter

[Karpenter](https://karpenter.sh/) is the standard node autoscaler for Kubernetes, replacing Cluster Autoscaler on AWS (EKS), Azure (AKS NAP), and generic clusters.

### What Radar Shows

**Topology:** Full provisioning chain — NodePool → NodeClaim → Node → Pod. See which NodePool owns which NodeClaims, which Nodes they provisioned, and what Pods are running on them. NodePool → NodeClass edges show the provider-specific configuration each pool uses.

<p align="center">
  <img src="screenshots/integrations/karpenter-topology.png" alt="Karpenter Topology" width="800">
  <br><em>Karpenter in Topology View — NodePool → NodeClaim provisioning chain</em>
</p>

**NodePool Detail View:**
- Status conditions (Ready)
- Clickable NodeClass reference (EC2NodeClass, AKSNodeClass, or generic)
- Resource limits (CPU, memory)
- Disruption policy and consolidation settings
- Instance requirements (types, zones, architectures)
- Template labels applied to provisioned nodes

<p align="center">
  <img src="screenshots/integrations/karpenter-nodepool-detail.png" alt="NodePool Detail" width="800">
  <br><em>NodePool Detail View — Status, related NodeClaims, and full specification</em>
</p>

**NodeClaim Detail View:**
- Provisioning timeline with timestamps
- Status conditions (Initialized, Launched, Registered, Ready)
- Instance type, capacity, and zone
- Requirements (instance types, architectures, OS)
- Clickable Node and NodeClass references

**NodeClass Detail View** (EC2NodeClass, AKSNodeClass, etc.):
- AMI selector terms and aliases
- Block device mappings (volume type, size, encryption)
- IAM role configuration
- Subnet and security group discovery tags
- Instance metadata options (IMDS configuration)

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
| EC2NodeClass | `karpenter.k8s.aws/v1` | Yes | Yes | Yes |
| AKSNodeClass | `karpenter.azure.com/v1alpha2` | Yes | Generic | Yes |
| GCENodeClass | `karpenter.k8s.gcp/v1alpha1` | Yes | Generic | Yes |

All provider-specific NodeClass variants are automatically detected and supported.

---

## Cluster API (CAPI)

[Cluster API](https://cluster-api.sigs.k8s.io/) is the Kubernetes sub-project for declarative cluster lifecycle management. Used by platform teams to provision and manage workload clusters.

### What Radar Shows

**Topology:** Full CAPI ownership chain — ClusterClass → Cluster → KubeadmControlPlane → Machine → Node, and Cluster → MachineDeployment → MachineSet → Machine → Node. MachineHealthCheck → Cluster protection edges. Machine → Node edges use status.nodeRef (semantic, not owner-ref).

**Cluster Detail View:**
- Phase, version, cluster class, control plane endpoint
- Control plane and worker replica counts (v1beta2-aware)
- Control plane and infrastructure references (clickable)
- ClusterClass topology section (worker MachineDeployments table)
- "Connect to Cluster" button — auto-connects Radar to the workload cluster
- "Download Kubeconfig" button
- Conditions

**Machine Detail View:**
- Phase, role (Control Plane / Worker), version, provider ID
- Clickable Node reference (via status.nodeRef)
- Addresses table, node info (OS, architecture, kernel, kubelet)
- Bootstrap and infrastructure references

**MachineDeployment Detail View:**
- Phase, replicas (desired/ready/available/up-to-date), strategy
- Version, cluster name
- Machine template references
- Owned machines label hint (copyable)

**KubeadmControlPlane Detail View:**
- Replicas, version, initialized status (v1beta2-aware)
- Machine template with drain/volume detach/deletion timeouts
- Kubeadm config highlights (cert SANs)
- Last remediation info
- Owned machines label hint

**ClusterClass Detail View:**
- Infrastructure, control plane, worker topology tables
- Variables with schema types
- Patches with definitions and enabledIf expressions

**MachineHealthCheck Detail View:**
- Expected/healthy machine counts, remediations allowed
- Label selector display
- Unhealthy conditions tables (v1beta1 + v1beta2 formats)
- Remediation template

**Additional renderers:** MachineSet, MachinePool, MachineDrainRule, KubeadmConfig/Template

**Resource Browser:** Smart columns for all CAPI kinds — phase badges, replica counts, cluster names, roles, versions.

**Topology-controlled badge:** Resources managed by ClusterClass (label `topology.cluster.x-k8s.io/owned`) show a warning banner.

**Fleet topology mode:** Dedicated "Fleet" view filters to CAPI and infrastructure provider resources only, giving a clean cluster-management view without application workload noise. Groups start expanded by default.

![CAPI Fleet Topology — 5 GKE clusters with MachineDeployments, MachinePools, and provider resources](images/capi/fleet-topology.png)

**Resource browser** with smart columns per CAPI kind — Provider detection, phase badges, replica counts:

![CAPI Cluster list with Provider column](images/capi/cluster-list.png)

**Cluster detail view** with Connect to Cluster and Download Kubeconfig actions, provider detection, and clickable references to infrastructure resources:

![Cluster detail with Connect button and provider references](images/capi/cluster-detail.png)

### Infrastructure Provider Renderers

Radar has first-class renderers for **AWS (CAPA)**, **GCP (CAPG)**, and **Azure (CAPZ)** infrastructure provider resources. These surface provider-specific operational data — instance types, scaling config, VPC/subnet topology, managed service addons — that would otherwise be buried in raw YAML.

**AWS EKS control plane** — VPC topology with subnets (Public/Private badges), security groups, EKS addons, IAM roles:

![AWSManagedControlPlane with VPC, subnets, and IAM details](images/capi/aws-controlplane.png)

**GCP GKE control plane** — project, location, release channel, and conditions timeline with left-aligned timestamps:

![GCPManagedControlPlane with conditions timeline](images/capi/gcp-controlplane.png)

**Managed machine pools**: Instance/VM types, scaling config (autoscaling min/max), capacity type badges (On-Demand/Spot), node management (auto-repair/upgrade), labels and taints.

**Azure AKS**: Location, resource group, SKU tier, network plugin/policy, System/User mode badges, Regular/Spot priority, availability zones.

**Individual machines**: Instance type/state badges, provider IDs, addresses, conditions.

**Templates and cluster stubs**: Lightweight renderers for instance templates (with resolved capacity) and cluster infrastructure stubs (endpoint + failure domains).

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| Cluster | `cluster.x-k8s.io` | Yes | Yes | Yes |
| ClusterClass | `cluster.x-k8s.io` | Yes | Yes | Yes |
| Machine | `cluster.x-k8s.io` | Yes | Yes | Yes |
| MachineSet | `cluster.x-k8s.io` | Yes | Yes | Yes |
| MachineDeployment | `cluster.x-k8s.io` | Yes | Yes | Yes |
| MachinePool | `cluster.x-k8s.io` | Yes | Yes | Yes |
| MachineHealthCheck | `cluster.x-k8s.io` | Yes | Yes | Yes |
| MachineDrainRule | `cluster.x-k8s.io` | No | Yes | No |
| KubeadmControlPlane | `controlplane.cluster.x-k8s.io` | Yes | Yes | Yes |
| KubeadmControlPlaneTemplate | `controlplane.cluster.x-k8s.io` | No | Generic | No |
| KubeadmConfig | `bootstrap.cluster.x-k8s.io` | No | Yes | No |
| KubeadmConfigTemplate | `bootstrap.cluster.x-k8s.io` | No | Generic | No |
| AWSManagedControlPlane | `controlplane.cluster.x-k8s.io` | Yes | Yes | No |
| AWSManagedMachinePool | `infrastructure.cluster.x-k8s.io` | Yes | Yes | No |
| AWSMachine | `infrastructure.cluster.x-k8s.io` | Yes | Yes | No |
| AWSMachineTemplate | `infrastructure.cluster.x-k8s.io` | No | Yes | No |
| AWSManagedCluster | `infrastructure.cluster.x-k8s.io` | No | Yes | No |

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

**TriggerAuthentication Detail View:**
- Pod identity provider and configuration
- Secret references with linked Secret navigation
- Environment variable mappings
- External secret providers (HashiCorp Vault, Azure Key Vault, AWS Secrets Manager)

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
| TriggerAuthentication | `keda.sh/v1alpha1` | — | Yes | Yes |
| ClusterTriggerAuthentication | `keda.sh/v1alpha1` | — | Yes | Yes |

---

## Vertical Pod Autoscaler (VPA)

[VPA](https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler) automatically adjusts CPU and memory requests/limits for pods based on observed usage.

### What Radar Shows

**Topology:** VPA nodes appear in the Resources view with `EdgeUses` edges to target workloads, grouped in the Scalers section alongside HPA and KEDA.

**Detail View:** Target workload, update mode, per-container resource recommendations (target, lower bound, upper bound, uncapped), resource policy, and conditions.

**Problem Detection:** Alerts for unsupported configurations, missing recommendations, and low confidence scores.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| VerticalPodAutoscaler | `autoscaling.k8s.io/v1` | Yes | Yes | — |

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

**GatewayClass Detail View:** Controller name, description, parameters reference, and status conditions.

**HTTPRoute Detail View:** Rules with path/header matching, backend references, filters, and weights.

**GRPCRoute Detail View:** Service/method matching, backend references, and filters.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| GatewayClass | `gateway.networking.k8s.io/v1` | Yes | Yes | Yes |
| Gateway | `gateway.networking.k8s.io/v1` | Yes | Yes | Yes |
| HTTPRoute | `gateway.networking.k8s.io/v1` | Yes | Yes | Yes |
| GRPCRoute | `gateway.networking.k8s.io/v1` | Yes | Yes | Yes |
| TCPRoute | `gateway.networking.k8s.io/v1alpha2` | Yes | Yes | Yes |
| TLSRoute | `gateway.networking.k8s.io/v1alpha2` | Yes | Yes | Yes |

---

## Traefik

[Traefik](https://traefik.io/) is a modern reverse proxy and ingress controller for Kubernetes, with dynamic configuration, middleware chains, and advanced traffic management via CRDs.

### What Radar Shows

**Topology:** Full Traefik routing path — IngressRoute → Middleware → Service (or TraefikService → Service) with TLS and transport configuration edges. See how traffic flows from entrypoints through middleware chains and weighted/mirroring TraefikServices to backend Kubernetes Services. Both **Resources** and **Traffic** view modes are supported.

**IngressRoute / IngressRouteTCP / IngressRouteUDP Detail View:**
- Entry points and TLS configuration (secret, cert resolver, TLS options/stores)
- Route match expressions with priority and kind badges
- Per-route services with port, weight, and ServersTransport links
- Per-route middleware references with cross-namespace indicators
- Aggregated middleware chain with numbered ordering
- Alert banners for no-route or no-service configurations

**Resource Browser:** Smart columns show entry points, hosts (extracted from match expressions), route summaries, TLS status, and middleware counts. All 10 Traefik kinds have dedicated table columns — Middleware shows type, TraefikService shows type and targets, ServersTransport shows insecure/serverName, TLSOption shows min TLS version.

**Middleware / MiddlewareTCP Detail View:** type-aware rendering — `chain` links its member middlewares, auth middlewares (basicAuth/digestAuth/forwardAuth) show **secret references only, never values**, `errors` shows its service and status mapping, and unknown/plugin types fall back to a key/value view with nested credential keys redacted.

**TraefikService Detail View:** weighted backends with weight bars, mirroring (primary plus mirrors with percentages), and the load-balancing strategy.

**TLSOption Detail View:** minimum/maximum TLS versions, cipher suites, and client-auth configuration.

**ServersTransport / ServersTransportTCP Detail View:** SNI, an `insecureSkipVerify` warning banner, CA/client-cert secret references, and timeouts.

**Secret safety:** inline credentials in a Traefik CRD spec are redacted in the AI/MCP context (`get_resource`) — credential keys and high-confidence secret patterns are masked — and the Middleware detail renderer shows secret *references* only, never values. Reference names (`secretName`, `basicAuth.secret`, `rootCAsSecrets`) are preserved so you can still diagnose a bad reference. The raw YAML view still shows the object as stored in the cluster.

### Cluster Audit checks

Traefik ships no admission webhook or linter, so a typo'd reference is accepted silently and the route or middleware just doesn't do what you wrote. Radar's Cluster Audit catches the common dangling-reference cases as **Reliability** checks:

| Check | What it catches |
|-------|-----------------|
| `traefikRouteMissingService` | An IngressRoute referencing a Service or TraefikService that doesn't exist |
| `traefikRouteMissingMiddleware` | An IngressRoute referencing a Middleware that doesn't exist |
| `traefikChainMissingMiddleware` | A `chain` Middleware listing a member Middleware that doesn't exist |
| `traefikErrorsMissingService` | An `errors` Middleware whose `errors.service` references a Service that doesn't exist |

These checks are **inventory-authoritative**: a "missing X" finding is asserted only when that target kind is served by a cluster-wide synced informer, so a namespace-scoped (RBAC-limited) cache skips the check rather than fabricating a false positive. Matching is group-aware — a `traefik.io` reference is not satisfied by a `traefik.containo.us` object — and cross-namespace references resolve correctly.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| IngressRoute | `traefik.io/v1alpha1` | Yes | Yes | — |
| IngressRouteTCP | `traefik.io/v1alpha1` | Yes | Yes | — |
| IngressRouteUDP | `traefik.io/v1alpha1` | Yes | Yes | — |
| Middleware | `traefik.io/v1alpha1` | Yes | Yes | — |
| MiddlewareTCP | `traefik.io/v1alpha1` | Yes | Yes | — |
| TraefikService | `traefik.io/v1alpha1` | Yes | Yes | — |
| ServersTransport | `traefik.io/v1alpha1` | Yes | Yes | — |
| ServersTransportTCP | `traefik.io/v1alpha1` | Yes | Yes | — |
| TLSOption | `traefik.io/v1alpha1` | Yes | Yes | — |
| TLSStore | `traefik.io/v1alpha1` | Yes | Generic | — |

The legacy `traefik.containo.us` API group (pre-v2.11) is warm-listed alongside `traefik.io` so older clusters don't pay first-list latency.

---

## Contour

[Contour](https://projectcontour.io/) is a Kubernetes ingress controller using Envoy proxy, providing a powerful HTTPProxy CRD with route delegation, weighted routing, TLS termination, and TCP proxying.

### What Radar Shows

**Topology:** Full Contour routing path — HTTPProxy (root) → HTTPProxy (child, via delegation) → Service, with TLS secret configuration edges. Root proxies with `spec.virtualhost` appear as entry points; child proxies referenced via `spec.includes` are connected via delegation edges. Both **Resources** and **Traffic** view modes are supported.

<p align="center">
  <img src="screenshots/integrations/contour-topology.png" alt="Contour Topology" width="800">
  <br><em>Contour in Topology View — HTTPProxy → Service routing with delegation</em>
</p>

**HTTPProxy Detail View:**
- Status banner for invalid or orphaned proxies
- Virtual host FQDN and TLS configuration with clickable Secret links
- Routes with prefix/header conditions and backend services (name, port, weight)
- Delegation includes with cross-namespace indicators and condition prefixes
- TCP proxy services for passthrough configurations
- Status conditions (Valid/Invalid/Orphaned)

**Resource Browser:** Smart columns show FQDN, route count, include count, TLS status (shield icon), and validity status at a glance.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| HTTPProxy | `projectcontour.io/v1` | Yes | Yes | Yes |

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
| Certificate | `cert-manager.io/v1` | Yes | Yes | — |
| CertificateRequest | `cert-manager.io/v1` | Yes | Yes | — |
| Issuer | `cert-manager.io/v1` | Yes | Yes | — |
| ClusterIssuer | `cert-manager.io/v1` | Yes | Yes | — |
| Order | `acme.cert-manager.io/v1` | Yes | Yes | — |
| Challenge | `acme.cert-manager.io/v1` | Yes | Yes | — |

---

## Prometheus Operator

[Prometheus Operator](https://prometheus-operator.dev/) simplifies Prometheus setup on Kubernetes, providing CRDs for defining monitoring targets, alerting rules, and scrape configurations declaratively.

### What Radar Shows

**ServiceMonitor Detail View:**
- Status conditions
- Job label and scrape endpoint configuration (port, path, interval, scheme)
- Service selector (matchLabels)
- Namespace selector scope

**PrometheusRule Detail View:**
- Rule group breakdown with per-group rule counts
- Alert rules vs recording rules summary
- Group evaluation intervals

**PodMonitor Detail View:**
- Pod metrics endpoint configuration (port, path, interval, scheme)
- Pod selector (matchLabels)
- Namespace selector scope

**Resource Browser:** Smart columns show status, endpoint count, selectors, and job labels at a glance.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| ServiceMonitor | `monitoring.coreos.com/v1` | Service selection and scrape configuration | Yes | — |
| PodMonitor | `monitoring.coreos.com/v1` | Pod selection and scrape configuration | Yes | — |
| PrometheusRule | `monitoring.coreos.com/v1` | — | Yes | — |
| Alertmanager | `monitoring.coreos.com/v1` | — | Generic | — |

---

## Trivy Operator

[Trivy Operator](https://aquasecurity.github.io/trivy-operator/) continuously scans your cluster for vulnerabilities, misconfigurations, exposed secrets, and license compliance issues.

### What Radar Shows

**VulnerabilityReport Detail View:** Severity breakdown (Critical/High/Medium/Low), affected images, and CVE counts.

**ConfigAuditReport Detail View:** Pass/fail checks with severity levels.

**Resource Browser:** Smart columns show severity counts and scan status at a glance.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| VulnerabilityReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |
| ConfigAuditReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |
| ExposedSecretReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |
| ClusterComplianceReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |
| SbomReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |
| RbacAssessmentReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |
| ClusterRbacAssessmentReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |
| InfraAssessmentReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |
| ClusterInfraAssessmentReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |
| ClusterSbomReport | `aquasecurity.github.io/v1alpha1` | — | Yes | — |

---

## Bitnami Sealed Secrets

[Sealed Secrets](https://sealed-secrets.netlify.app/) encrypts Kubernetes Secrets so they can be safely stored in Git. The controller decrypts them in-cluster at deploy time.

### What Radar Shows

**Topology:** SealedSecret → Secret → workload when Secret metadata is visible, or SealedSecret → workload when Secret reads are restricted. Only SealedSecrets that configure a visible workload are included.

**SealedSecret Detail View:** Encrypted data keys, template metadata, and the target Secret's scope and namespace.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| SealedSecret | `bitnami.com/v1alpha1` | Yes | Yes | — |

---

## GitOps

See the main [README](../README.md#gitops) for the user-facing overview. This section covers integration coverage and capabilities.

### FluxCD

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| GitRepository | `source.toolkit.fluxcd.io/v1` | Yes | Yes | — |
| OCIRepository | `source.toolkit.fluxcd.io/v1beta2` | Yes | Yes | — |
| HelmRepository | `source.toolkit.fluxcd.io/v1` | Yes | Yes | — |
| Kustomization | `kustomize.toolkit.fluxcd.io/v1` | Yes | Yes | Yes |
| HelmRelease | `helm.toolkit.fluxcd.io/v2` | Yes | Yes | Yes |
| Alert | `notification.toolkit.fluxcd.io/v1beta3` | — | Yes | — |

**Workflow operations**: Reconcile, Reconcile-with-source (Kustomization/HelmRelease), Suspend/Resume.

**Diagnosis**: Conditions extracted to issues (Ready=False, Stalled=True, Reconciling=True). Per-resource diff and recent events not yet available for Flux (HelmRelease-installed resources don't carry `last-applied-configuration`; tracked in [#601](https://github.com/skyhook-io/radar/issues/601)).

### ArgoCD

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| Application | `argoproj.io/v1alpha1` | Yes | Yes | Yes |
| ApplicationSet | `argoproj.io/v1alpha1` | — | Generic | — |
| AppProject | `argoproj.io/v1alpha1` | — | Generic | — |

**Workflow operations**: Sync (with options dialog: prune, dry-run, apply-only, force, replace, server-side apply, sync-options), Refresh, Hard refresh, Terminate, Suspend/Resume auto-sync, Rollback to historical revision, Selective sync of marked resources.

**Diagnosis**:
- **Per-resource field diff** computed from each resource's `kubectl.kubernetes.io/last-applied-configuration` annotation vs live spec — works for any Argo client-side-applied resource without calling the Argo API
- **Recent events** surfaced inline per managed resource (5 most recent, namespace-RBAC-filtered)
- **Stuck-drift-loop detector** — flags `sync=OutOfSync ∧ opPhase=Succeeded ∧ auto-sync on ∧ reconciledAt<30min` with the likely cause (mutating webhook, sibling controller, schema migration)
- **Manual-drift detector** — calls out OutOfSync apps with auto-sync disabled
- **Argo Application conditions** extracted to issues (ComparisonError, OrphanedResourceWarning, etc.) with type-aware severity and per-condition action text
- **Operation-failure parser** recognizes 11 patterns: annotation-too-large, label-too-long, hook failure, admission webhook denial, RBAC, conflict, immutable field, schema migration, connectivity, etc.

**Limitations**:
- SSA-applied resources (`ServerSideApply=true` sync-option) lack the `last-applied-configuration` annotation; per-resource diff is unavailable for those rows. SSA fallback via `metadata.managedFields` and Argo API integration for canonical Git-rendered diffs are tracked in [#601](https://github.com/skyhook-io/radar/issues/601).
- Single-cluster only: Application↔resource edges only render when Radar is connected to the cluster where the managed resources live (not necessarily the cluster running the Argo controller).

---

## Argo Rollouts

[Argo Rollouts](https://argoproj.github.io/rollouts/) provides progressive delivery strategies including blue-green and canary deployments.

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| Rollout | `argoproj.io/v1alpha1` | Yes | Yes | Yes |

---

## Argo Workflows

[Argo Workflows](https://argoproj.github.io/workflows/) is a container-native workflow engine for orchestrating parallel jobs on Kubernetes.

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| Workflow | `argoproj.io/v1alpha1` | — | Yes | — |
| WorkflowTemplate | `argoproj.io/v1alpha1` | — | Yes | — |
| CronWorkflow | `argoproj.io/v1alpha1` | — | Generic | — |

---

## Istio

[Istio](https://istio.io/) is the most widely adopted service mesh, providing traffic management, security (mTLS), and observability for microservices.

### What Radar Shows

**Topology:** Full Istio traffic path — IstioGateway → VirtualService → Service, and DestinationRule → Service configuration edges. See how traffic flows through gateway listeners, virtual service routing rules, and into backend services.

**VirtualService Detail View:**
- HTTP/TCP/TLS routing rules with match conditions
- Destinations with weight distribution bars
- Fault injection and traffic mirroring detection (AlertBanner warnings)
- Retry policies, timeouts, and CORS settings
- Gateway references with clickable links

**DestinationRule Detail View:**
- Target service host with clickable link
- Traffic policy: connection pool (TCP/HTTP limits), load balancer algorithm, outlier detection (ejection settings), TLS mode
- Subset definitions with labels and per-subset traffic policy overrides

**Gateway Detail View (networking.istio.io):**
- Server configurations with port, protocol, and hosts
- TLS settings per server (mode, credential references)
- Workload selector labels

**ServiceEntry Detail View:**
- Hosts, location (MESH_EXTERNAL/MESH_INTERNAL), resolution strategy
- Ports with protocol badges
- Endpoint addresses with port mappings and labels

**PeerAuthentication Detail View:**
- mTLS mode with color-coded badges (STRICT/PERMISSIVE/DISABLE)
- Scope indicator (workload-scoped vs namespace-wide)
- Port-level mTLS overrides

**AuthorizationPolicy Detail View:**
- Action badge (ALLOW/DENY/CUSTOM/AUDIT) with rule breakdown
- Source principals, namespaces, IP blocks
- Operation matching (hosts, ports, methods, paths)
- Deny-all and allow-nothing detection (AlertBanner)

**Resource Browser:** Smart columns show status badges, hosts, gateways, route counts, mTLS modes, actions, and load balancer algorithms at a glance.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| VirtualService | `networking.istio.io/v1` | Yes | Yes | — |
| DestinationRule | `networking.istio.io/v1` | Yes | Yes | — |
| Gateway | `networking.istio.io/v1` | Yes | Yes | — |
| ServiceEntry | `networking.istio.io/v1` | — | Yes | — |
| PeerAuthentication | `security.istio.io/v1` | — | Yes | — |
| AuthorizationPolicy | `security.istio.io/v1` | — | Yes | — |

---

## Velero

[Velero](https://velero.io/) provides backup and restore capabilities for Kubernetes cluster resources and persistent volumes.

### What Radar Shows

**Backup Detail View:**
- Phase with color-coded badge, start/completion timestamps, duration
- Progress bar during in-progress backups (items backed up percentage)
- Scope filters: included/excluded namespaces and resources, label selectors
- Storage location and volume snapshot locations
- Options: TTL, snapshot volumes, default filesystem backup
- Error/warning detection (AlertBanner for failed or partial backups with validation errors)

**Restore Detail View:**
- Phase badge, source backup reference, duration
- Progress bar during in-progress restores
- Scope filters: included/excluded namespaces and resources
- Restore options: PV restoration, existing resource policy
- Error detection (AlertBanner for failed or partial restores)

**Schedule Detail View:**
- Cron schedule (monospace), last backup timestamp
- Pause state detection (AlertBanner when paused)
- Validation failure detection (AlertBanner)
- Backup template: storage location, TTL, namespace/resource filters, snapshot settings

**BackupStorageLocation Detail View:**
- Phase (Available/Unavailable), last validation and sync times
- Provider configuration: bucket, prefix, region, access mode
- Provider-specific config key-value pairs

**VolumeSnapshotLocation Detail View:**
- Provider name and configuration parameters

**Resource Browser:** Smart columns show phase badges, storage location, namespace counts, duration, expiry (with color-coded warnings), and error/warning counts.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| Backup | `velero.io/v1` | — | Yes | — |
| Restore | `velero.io/v1` | — | Yes | — |
| Schedule | `velero.io/v1` | — | Yes | — |
| BackupStorageLocation | `velero.io/v1` | — | Yes | — |
| VolumeSnapshotLocation | `velero.io/v1` | — | Yes | — |

---

## External Secrets Operator

[External Secrets Operator](https://external-secrets.io/) (ESO) synchronizes secrets from external providers (AWS Secrets Manager, HashiCorp Vault, Azure Key Vault, GCP Secret Manager, and more) into Kubernetes Secrets.

### What Radar Shows

**ExternalSecret Detail View:**
- Sync status badge, last sync time, refresh interval
- Store reference with clickable link and kind indicator
- Secret mappings table (secret key → remote key, property, version)
- Data sources with type badges
- Target secret configuration and creation policies
- Sync failure detection (AlertBanner when Ready condition is False)

**ClusterExternalSecret Detail View:**
- Overview: provisioned vs failed namespace counts
- Namespace selection: explicit list or label selector
- Provisioned namespaces (green badges)
- Failed namespaces with per-namespace error details (AlertBanner)
- ExternalSecret spec: refresh interval, store reference, data/source counts

**SecretStore / ClusterSecretStore Detail View:**
- Provider with color-coded badge (AWS orange, Azure/GCP blue, Vault purple, etc.)
- Provider-specific details: region, vault URL, project ID, authentication method
- Connection status with reason and last transition
- Retry settings
- Readiness detection (AlertBanner when not Ready)

**Resource Browser:** Smart columns show sync status, store reference, provider type, refresh interval, and last sync time.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| ExternalSecret | `external-secrets.io/v1beta1` | — | Yes | — |
| ClusterExternalSecret | `external-secrets.io/v1beta1` | — | Yes | — |
| SecretStore | `external-secrets.io/v1beta1` | — | Yes | — |
| ClusterSecretStore | `external-secrets.io/v1beta1` | — | Yes | — |

---

## CloudNativePG

[CloudNativePG](https://cloudnative-pg.io/) (CNPG) is the Kubernetes operator for PostgreSQL, covering the full lifecycle from bootstrapping to monitoring, with high availability, automated failover, and backup management.

### What Radar Shows

**Cluster Detail View:**
- Phase, instances ready/desired, primary instance, image version
- Instance node distribution (which K8s nodes run each PostgreSQL instance)
- Storage configuration: data size, storage class, WAL storage
- Backup configuration: destination, retention policy, last successful backup, recovery point
- Monitoring: PodMonitor integration, custom query ConfigMaps
- Replication settings (for replica clusters)
- PostgreSQL parameters
- Health detection (AlertBanner for degraded clusters, failover/switchover in progress)

**Backup Detail View:**
- Phase, backup method, duration, start/stop timestamps
- Cluster reference with clickable link
- Destination path and server name
- Recovery target
- Failure detection (AlertBanner with error message)

**ScheduledBackup Detail View:**
- Cron schedule, last/next schedule timestamps
- Suspension detection (AlertBanner when paused)
- Backup configuration: cluster reference, method, owner reference settings

**Pooler Detail View:**
- Type (read-write/read-only) with colored badge, pool mode
- Instances ready/desired
- Cluster reference with clickable link
- PgBouncer parameters
- Degraded state detection (AlertBanner when not all instances ready)

**Resource Browser:** Smart columns show status, instance counts (with degraded highlighting), primary instance, image tag, storage size, cluster reference, and schedule expressions.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| Cluster | `postgresql.cnpg.io/v1` | — | Yes | — |
| Backup | `postgresql.cnpg.io/v1` | — | Yes | — |
| ScheduledBackup | `postgresql.cnpg.io/v1` | — | Yes | — |
| Pooler | `postgresql.cnpg.io/v1` | — | Yes | — |

---

## Crossplane

[Crossplane](https://crossplane.io/) extends Kubernetes with declarative cloud-resource management. Operators define platform APIs (`CompositeResourceDefinition`s + `Composition`s), and provider packages reconcile real cloud resources from `Managed Resource` CRs in any cloud or SaaS. Radar treats every provider as a first-class integration without needing per-provider code — detection is heuristic, based on spec shape.

### What Radar Shows

**Sidebar:** All Crossplane resources land under a single "Crossplane" group, including provider-shipped MR groups (`*.upbound.io`, `*.crossplane.io` subgroups). Provider-Kubernetes and Provider-Helm config groups are first-class.

**Managed Resource Detail View** (the generic MR renderer — works for every provider, including Upbound AWS/GCP/Azure, provider-kubernetes, provider-helm, and any community provider):
- Kind, API group, external-name annotation, deletion + management policies
- Paused banner (`crossplane.io/paused: "true"`) — reconciliation suppressed by operator intent
- Alert banner when `Synced=False` or `Ready=False` with the upstream cloud error verbatim
- Linked `ProviderConfig` and (when this MR is composed) linked parent Composite via owner-ref walk
- Collapsed `spec.forProvider` and `status.atProvider` JSON for deep diagnosis

**Composite / Claim Detail View** — the killer feature:
- Linked Composition and CompositionRevision (when pinned)
- **Composed Resources list** — every entry in `spec.crossplane.resourceRefs` (v2) or `spec.resourceRefs` (v1) rendered as a clickable row with **its own live status badge**, fetched per-row via React Query. Clicking opens the composed resource's drawer.
- Paused banner when `crossplane.io/paused: "true"`
- For v1 Claims: linked bound XR

**Composition Detail View:**
- Mode badge (`Pipeline` violet vs `Resources` neutral)
- Backed by linked XRD
- Pipeline mode: numbered step cards, each with linked Function package, input kind, and expandable raw input
- Resources mode: list of composed-resource templates with patch counts

**XRD Detail View:**
- Generated CR section: kind, plural, group, scope (v2 only — `Cluster` vs `Namespaced` badge)
- Claim names (v1)
- Versions table with `served` / `referenceable` / `deprecated` badges
- Default + enforced Composition links
- Connection-secret keys
- `Established` / `Offered` conditions

**Provider / Function / Configuration Detail View** (shared renderer):
- Package OCI image, pull policy, revision activation policy
- Current revision + identifier
- Linked DeploymentRuntimeConfig (when set)
- Linked package dependencies
- For Configurations: list of installed XRDs/Compositions/Functions from `status.objectRefs`
- Alert banners for `Installed=False` (install failed) or `Healthy=False` (controller unhealthy)

**ProviderConfig Detail View:**
- API group, credentials source (`InjectedIdentity`, `Secret`, etc.)
- "N in use" status badge from `status.users`
- Linked credentials Secret when applicable

**Resource Browser:** MR list shows kind / external name / provider config / status; Provider list shows package, revision, status; Composition list shows mode, composite kind, function count; XRD list shows generated kind and claim kind.

**Cluster Audit:** New `crossplaneStuck` check flags MRs/XRs/Claims reporting `Ready=False` or `Synced=False` for more than 5 minutes (warning) or 30 minutes (danger). Synced=False takes priority over Ready=False because it usually indicates the actionable problem (bad ProviderConfig, malformed spec, missing IAM). Same severity ramp as `stuckTerminating` for cross-surface consistency. Paused resources are deliberately suppressed.

### v1 vs v2 Path Handling

Crossplane v2 moved several fields under `spec.crossplane.*`. Radar's renderers and detectors check the v2 path first, fall back to v1 — no version detection needed. Fields handled this way:

- `spec.crossplane.providerConfigRef` ↔ `spec.providerConfigRef`
- `spec.crossplane.resourceRefs` ↔ `spec.resourceRefs`
- `spec.crossplane.compositionRef` ↔ `spec.compositionRef`
- `spec.crossplane.compositionRevisionRef` ↔ `spec.compositionRevisionRef`
- `spec.crossplane.managementPolicies` ↔ `spec.managementPolicies`
- `spec.crossplane.deletionPolicy` ↔ `spec.deletionPolicy`

### Detection Heuristic (How Generic Renderers Match)

- **Managed Resource**: presence of `spec.providerConfigRef` (v1 or v2 path)
- **Composite / Claim**: presence of `spec.resourceRefs` (v1 or v2 path) AND not an MR
- **v1 Claim**: also has `spec.resourceRef` (singular, pointing at the bound XR) + `spec.compositionRef`

The set of MR CRD kinds is unbounded — every provider ships its own. Detection by spec shape lets Radar handle providers it has never seen without per-provider code.

### RBAC

The Helm chart's `rbac.crdGroups.crossplane: true` toggle grants read access to:
- `crossplane.io`, `pkg.crossplane.io`, `apiextensions.crossplane.io` (Crossplane core)
- `kubernetes.crossplane.io`, `helm.crossplane.io` (provider-kubernetes + provider-helm — useful in non-cloud installs)

For Upbound provider CRDs (`s3.aws.upbound.io`, `compute.gcp.upbound.io`, etc.), list them in `rbac.additionalCrdGroups` — Kubernetes RBAC has no `apiGroups` wildcards. Alternative: set `rbac.crdGroups.all: true` to grant cluster-wide read on every CRD (simpler, broader).

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| Managed Resources (any provider) | `*.upbound.io`, `kubernetes.crossplane.io`, `helm.crossplane.io`, `*.crossplane.io` | — | Yes | — |
| Composite Resources (XRs) | user-defined groups | — | Yes | — |
| Claims (v1) | user-defined groups | — | Yes | — |
| CompositeResourceDefinition | `apiextensions.crossplane.io/v1`, `v2` | — | Yes | — |
| Composition | `apiextensions.crossplane.io/v1` | — | Yes | — |
| CompositionRevision | `apiextensions.crossplane.io/v1` | — | Yes | — |
| Provider | `pkg.crossplane.io/v1` | — | Yes | — |
| Function | `pkg.crossplane.io/v1` | — | Yes | — |
| Configuration | `pkg.crossplane.io/v1` | — | Yes | — |
| ProviderConfig | per-provider group | — | Yes | — |

### Out of Scope

Deferred to a future "full Crossplane" pass:

- Topology edges (XR → composed MRs in the graph view)
- `Usage` / `ClusterUsage` rendering (delete-protection visualization)
- Cloud-console deep links from `external-name`
- Provider controller pod link with one-click log access
- Connection-secret link on XRs
- Mutating actions (force-reconcile, pause/unpause via `crossplane.io/paused`, manual sync)
- Composition revision diff view (compare adjacent `CompositionRevision`s)
- Per-XR insights pipeline (drift / events / plan / history surface — same shape as the GitOps detail page)
- Per-provider specialized renderers (e.g. an S3-specific section that calls out bucket policy / versioning) — generic MR renderer covers the daily need; specialize on user demand

---

## Kyverno

[Kyverno](https://kyverno.io/) is a Kubernetes-native policy engine for validation, mutation, generation, and image verification — no new language required, policies are written as Kubernetes resources.

### What Radar Shows

**Policy / ClusterPolicy Detail View:**
- Failure action badge (Enforce in red, Audit in yellow)
- Configuration: background scanning, webhook timeout, failure policy, schema validation
- Rule type summary (validate/mutate/generate/verifyImages counts)
- Individual rules with type badges and match/exclude indicators
- Auto-generated rules list

**PolicyReport / ClusterPolicyReport Detail View:**
- Visual result bar chart (pass/fail/warn/error/skip proportions)
- Scope and source information
- Individual results with status badges, severity levels, policy/rule names
- Expandable details: message, category, source, affected resources
- Problem detection (AlertBanner for failures or errors)

**Resource Browser:** Smart columns show status (colored by worst outcome), failure action, rule counts, and pass/fail/warn/error/skip breakdowns.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| Policy | `kyverno.io/v1` | — | Yes | — |
| ClusterPolicy | `kyverno.io/v1` | — | Yes | — |
| PolicyReport | `wgpolicyk8s.io/v1alpha2` | — | Yes | Yes |
| ClusterPolicyReport | `wgpolicyk8s.io/v1alpha2` | — | Yes | Yes |

PolicyReport findings are policy posture, not live operational failure, so they are **not** part of the `/api/issues` stream. They surface per-resource: the PolicyReport detail view (above) and the `resourceContext` policy rollup on a resource fetched via `get_resource`. (The cluster audit — `/api/audit` + MCP `get_cluster_audit` — is radar's own static best-practice scanner and does **not** include PolicyReport results.)

---

## Knative

[Knative](https://knative.dev/) extends Kubernetes with serverless capabilities: scale-to-zero, request-driven autoscaling, event-driven architectures, and simplified service deployment.

### What Radar Shows

**Topology:** Full Knative Serving chain — Route → KnativeService → Configuration → Revision → Deployment → Pod. Eventing flow — PingSource → Broker → Trigger → subscriber target. See how traffic is split across revisions, which configurations are active, and how events flow from sources through brokers to triggers.

<p align="center">
  <img src="screenshots/integrations/knative-topology.png" alt="Knative Topology" width="800">
  <br><em>Knative in Topology View — Serving chain and Eventing flow</em>
</p>

**KnativeService Detail View:**
- Status with URL and ingress readiness
- Latest ready and latest created revision links
- Scaling configuration (min/max scale, concurrency, timeout)
- Traffic split across revisions with percentage bars
- Container template (image, ports, env, resources)
- Conditions (Ready, RoutesReady, ConfigurationsReady)

<p align="center">
  <img src="screenshots/integrations/knative-service-detail.png" alt="Knative Service Detail" width="800">
  <br><em>KnativeService Detail View — URL, scaling, traffic splits, and conditions</em>
</p>

**Revision Detail View:**
- Container image with tag
- Concurrency model and container concurrency limit
- Timeout and scaling bounds (min/max)
- Traffic percentage (active vs inactive)
- Conditions (Ready, ContainerHealthy, ResourcesAvailable, Active)

**Route Detail View:**
- URL and domain
- Traffic targets with revision names and percentage distribution
- Conditions (Ready, AllTrafficAssigned, IngressReady)

**Configuration Detail View:**
- Latest created and latest ready revision references
- Generation tracking
- Conditions (Ready)

**Broker Detail View:**
- Address (internal URL for event delivery)
- Delivery configuration (dead letter sink, retry, backoff)
- Conditions (Ready, Addressable, FilterReady, IngressReady, TriggerChannelReady)

**Trigger Detail View:**
- Broker reference
- Subscriber target (service, URI, or Kubernetes reference)
- Event filter attributes
- Delivery configuration (dead letter sink)
- Conditions (Ready, BrokerReady, SubscriberResolved, DependencyReady)

**Source Detail Views (PingSource, ApiServerSource, ContainerSource, SinkBinding):**
- Sink target reference
- Source-specific configuration:
  - PingSource: cron schedule, data payload, content type
  - ApiServerSource: API resources watched, event mode, service account
  - ContainerSource: container image and arguments
  - SinkBinding: subject reference (Deployment, Job, etc.)
- Conditions (Ready, Deployed, SinkProvided)

**Networking Detail Views (Ingress, Certificate, ServerlessService):**
- KnativeIngress: ingress class, visibility, TLS hosts, rules with path/host routing
- KnativeCertificate: domain names, DNS names, not-after expiry
- ServerlessService: mode (Proxy/Serve), network status

**Flow Detail Views (Sequence, Parallel):**
- Sequence: ordered list of steps with subscriber references
- Parallel: branches with filter and subscriber configurations
- Reply/channel template settings

**Resource Browser:** Smart columns show status, URLs, latest revisions, traffic splits, schedules, sinks, brokers, subscribers, and filters at a glance.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| Service | `serving.knative.dev/v1` | Yes | Yes | — |
| Configuration | `serving.knative.dev/v1` | Yes | Yes | — |
| Revision | `serving.knative.dev/v1` | Yes | Yes | — |
| Route | `serving.knative.dev/v1` | Yes | Yes | — |
| DomainMapping | `serving.knative.dev/v1beta1` | — | Yes | — |
| Broker | `eventing.knative.dev/v1` | Yes | Yes | — |
| Trigger | `eventing.knative.dev/v1` | Yes | Yes | — |
| EventType | `eventing.knative.dev/v1beta2` | — | Yes | — |
| Channel | `messaging.knative.dev/v1` | — | Yes | — |
| InMemoryChannel | `messaging.knative.dev/v1` | — | Yes | — |
| Subscription | `messaging.knative.dev/v1` | — | Yes | — |
| PingSource | `sources.knative.dev/v1` | Yes | Yes | — |
| ApiServerSource | `sources.knative.dev/v1` | Yes | Yes | — |
| ContainerSource | `sources.knative.dev/v1` | Yes | Yes | — |
| SinkBinding | `sources.knative.dev/v1` | Yes | Yes | — |
| Sequence | `flows.knative.dev/v1` | — | Yes | — |
| Parallel | `flows.knative.dev/v1` | — | Yes | — |
| Ingress | `networking.internal.knative.dev/v1alpha1` | — | Yes | — |
| Certificate | `networking.internal.knative.dev/v1alpha1` | — | Yes | — |
| ServerlessService | `networking.internal.knative.dev/v1alpha1` | — | Yes | — |

## OpenCost

[OpenCost](https://www.opencost.io/) is a CNCF tool for Kubernetes cost monitoring, exposing cloud provider pricing and workload resource allocation as Prometheus metrics.

Radar discovers if OpenCost metrics are available in the already-discovered Prometheus. If OpenCost is installed and scraping into Prometheus, cost data appears automatically with no additional configuration. The integration is passive and read-only.

### What Radar Shows

**Resource Costs** 

**Dashboard Cost Card:** Cluster hourly cost and projected monthly cost, top 5 most expensive namespaces with a horizontal bar chart. Clicking navigates to the full Cost Insights view.

**Cost Insights View (`/cost`):**
- Header: cluster hourly/monthly cost, efficiency %, idle cost projection
- Resource cost split bar: CPU / Memory / Storage percentage breakdown
- Cost trend chart with 6h/24h/7d range selector and per-namespace hover tooltips
- Namespace breakdown table (sortable by cost, efficiency, CPU/memory split) — click any row to expand per-workload costs on demand
- Node costs table: instance type, region, and hourly/monthly pricing per machine
- Efficiency color coding: green (50%+), amber (25–50%), red (below 25%)

### Prerequisites

1. OpenCost (or Kubecost) deployed in your cluster, with its metrics being scraped by Prometheus

OpenCost cost data is not CRD-based — no custom resources are required. Cost views appear automatically when metrics are detected; they are hidden when no OpenCost metrics are found in Prometheus.

---

## Dynamic Resource Allocation (DRA)

[Dynamic Resource Allocation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/) is Kubernetes' structured device-allocation API (GA in 1.34), replacing opaque extended-resource integers for GPUs and other accelerators. These are built-in `resource.k8s.io` APIs, not CRDs — Radar discovers them automatically when the cluster serves the group.

### What Radar Shows

**ResourceClaim Detail View:** Allocation lifecycle status (Pending → Allocated → reserved), device requests with their DeviceClass (handles v1 `exactly`/`firstAvailable` and v1beta1 request shapes), allocated driver/pool/device results, reserved-for consumers with Pod links, and per-device health conditions when drivers report them. An allocated-but-unreserved claim is flagged — long-lived, it leaks a device.

**Related Resources:** Pods show their ResourceClaims (direct and template-generated); ResourceClaims link back to their DeviceClasses and the Pods holding the reservation.

**Resource Browser:** Claims list with allocation status, device class, allocated driver, and reservation columns. ResourceSlices list the per-node device inventory each driver publishes (driver, pool, node, device count).

### Supported Kinds

| Kind | Group | Topology | Detail View | AI Summary |
|------|-------|----------|-------------|------------|
| ResourceClaim | `resource.k8s.io` (v1, v1beta2) | Relationships | Yes | Yes |
| ResourceClaimTemplate | `resource.k8s.io` (v1, v1beta2) | — | Yes | Yes |
| DeviceClass | `resource.k8s.io` (v1, v1beta2) | Relationships | Yes | Yes |
| ResourceSlice | `resource.k8s.io` (v1, v1beta2) | — | Yes | Yes |

---

## NVIDIA GPU Operator

The [NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/) manages the GPU software stack (driver, container toolkit, device plugin, DCGM) on Kubernetes.

### What Radar Shows

**ClusterPolicy Detail View:** Operator state (ready / notReady / disabled), enabled components (driver, toolkit, device plugin, DCGM exporter, GFD, MIG manager, …), and MIG strategy. NVIDIA's ClusterPolicy shares its kind name with Kyverno's — Radar disambiguates by API group everywhere.

**NVIDIADriver Detail View:** Driver rollout state, type, version, image/repository, precompiled flag, and node selector.

### Supported CRDs

| CRD | Group | Topology | Detail View | AI Summary |
|-----|-------|----------|-------------|------------|
| ClusterPolicy | `nvidia.com/v1` | — | Yes | Yes |
| NVIDIADriver | `nvidia.com/v1alpha1` | — | Yes | Yes |

---

## Network Policies

[Network Policies](https://kubernetes.io/docs/concepts/services-networking/network-policies/) control pod-to-pod and pod-to-external traffic at the network level. Radar supports standard Kubernetes NetworkPolicy as well as Cilium's CiliumNetworkPolicy and CiliumClusterwideNetworkPolicy CRDs, providing visibility into what traffic is allowed, denied, and which workloads are unprotected.

### What Radar Shows

**Topology:** NetworkPolicy and CiliumNetworkPolicy nodes appear in the topology graph with edges connecting them to the workloads they protect. See at a glance which deployments have network policies applied and which are exposed.

<p align="center">
  <img src="screenshots/integrations/netpol-topology.png" alt="Network Policy Topology" width="800">
  <br><em>Network Policies in Topology View — policies connected to protected workloads</em>
</p>

**Policy Flow Diagram:** Each NetworkPolicy detail drawer includes a visual flow diagram showing ingress and egress rules as a directional graph — sources on the left, targets on the right, with ports and protocols labeled. Quickly understand what a policy allows without reading YAML.

<p align="center">
  <img src="screenshots/integrations/netpol-flow-diagram.png" alt="Policy Flow Diagram" width="600">
  <br><em>Policy Flow Diagram — visual representation of ingress and egress rules</em>
</p>

**Dashboard Coverage Card:** The home dashboard includes a Network Policy Coverage card showing total policy count, the percentage of workloads covered by at least one policy, and a count of uncovered workloads. Click through to browse all policies.

<p align="center">
  <img src="screenshots/integrations/netpol-dashboard-card.png" alt="Network Policy Coverage Card" width="400">
  <br><em>Dashboard Coverage Card — policy count, coverage percentage, and uncovered workloads</em>
</p>

**Cilium Policy Detail View:**
- Endpoint selector targeting
- Ingress/egress rules with allow and deny semantics
- Cilium-specific entity selectors (world, cluster, host)
- CIDR rules, port/protocol specifications
- Related workloads with clickable links

<p align="center">
  <img src="screenshots/integrations/netpol-cilium-renderer.png" alt="CiliumNetworkPolicy Detail" width="400">
  <br><em>CiliumNetworkPolicy Detail — endpoint selector, ingress deny from world, egress allow to cluster</em>
</p>

**Standard NetworkPolicy Detail View:**
- Pod selector and namespace selector rules
- Ingress and egress rules with CIDR blocks, ports, and protocols
- Policy type indicators (Ingress, Egress, or both)
- Related resources showing protected workloads

**Traffic View Integration:** When Hubble is available, dropped flows are correlated with the network policies that caused them, showing which policy denied specific traffic in real time.

<p align="center">
  <img src="screenshots/integrations/netpol-traffic-correlation.png" alt="Traffic Drop Correlation" width="800">
  <br><em>Traffic View — dropped flow with POLICY_DENIED reason and selecting policy correlation</em>
</p>

### Supported Resources

| Resource | Group | Topology | Detail View | AI Summary |
|----------|-------|----------|-------------|------------|
| NetworkPolicy | `networking.k8s.io/v1` | Yes | Yes | Yes |
| CiliumNetworkPolicy | `cilium.io/v2` | Yes | Yes | Yes |
| CiliumClusterwideNetworkPolicy | `cilium.io/v2` | Yes | Yes | Yes |

---

## Any Other CRD

Radar automatically discovers and displays **every** CRD installed in your cluster — no configuration or plugins required. Resources appear in the sidebar, can be filtered and searched, and show full YAML with syntax highlighting in the detail drawer. The integrations above add richer presentation, but every CRD is browsable out of the box.
