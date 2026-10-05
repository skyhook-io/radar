# Datum / Milo (experimental)

Datum is a read-only workspace for a project control plane, available in local Radar with your own kubeconfig. It composes public hostname, DNS, connector and network evidence across exact API groups. It also browses root/organization-plane Projects. No Datum write workflows, IAM/billing administration, token refresh or kubeconfig changes are provided.

The workspace follows the same contracts and shared seams as CloudNativePG: `ResourcesSidebar.categoryWorkspaces`, shared workspace coverage, table/layout and fact/problem primitives, context-preserving navigation, drawer trails and `WorkloadView.renderSummary`. An object's composed **Overview** is reachable from lists, topology, Issues and workspace rows; its detailed renderer moves to **Spec & status**. YAML remains available through the ordinary resource editor.

## Destinations and navigation

- **Hostnames** (default): one row per hostname declared by an HTTPProxy or Domain, with reported failures first. Verification, DNS programming, proxy programming, backend state and certificates are separate sourced facts. There is deliberately no aggregate “live” badge.
- **DNS**: zones, provisioned nameservers, Accepted/Programmed conditions, controller-reported record count versus observed record sets and their declared record entries; records with failed, pending or unknown reports.
- **Connectors**: readiness, capabilities, referenced lease evidence and proxies configured to use the connector. A lease timestamp is not a continuously observed heartbeat.
- **Networking**: Networks and observed NetworkContexts/Bindings/Subnets/Claims, NetworkServices and compute objects, with exact references and reported allocation/conditions.
- **Projects**: control-plane-wide inventory and runtime project navigation, independent of the namespace filter.

Workspace destinations appear above Datum resource kinds when those APIs are discovered. Ordinary per-kind Resources lists remain available. Namespaced screens follow the namespace picker; a full detail page reads its object's namespace and says when it falls outside the current filter. Drawer trails preserve API group, namespace and name. Full details carry `?ctx=`: switching connection cannot silently reopen a same-named object in a different control plane. Return navigation is bound to the reviewed context.

Paths: `/datum`, `/datum/{dns,connectors,networking,projects}`, and `/datum/<plural>/<namespace-or-_>/<name>?ctx=<connection>`. Foreign HTTPProxy, Workload, Role and ServiceAccount kinds retain their own exact-group dispatch.

## Evidence contract

| Fact | Source | Interpretation / unknown behavior |
|---|---|---|
| Domain ownership | `Domain.status.conditions[Verified]` | Alternative DNS/HTTP/zone verification methods are not independent readiness requirements. Missing or stale overall verification is unknown. |
| Domain validity | `ValidDomain` | A current False condition is actionable; `PendingVerification` is reconciling. |
| Domain association | Longest matching domain suffix, in the same namespace | Inferred; no ownership/reference or DNS proof is implied. |
| Zone association | `DNSZone.status.domainRef` | Controller-recorded reference; no suffix join is presented as an exact link. |
| DNS programming | Zone/set `Programmed`, nested `RecordProgrammed`, proxy hostname `DNSRecordProgrammed` when reported | Configuration evidence, not client DNS resolution. Unobserved hostname records stay unknown. |
| Nameservers | Zone `status.nameservers` and Domain `status.nameservers` | Reported provisioning/registration evidence; actual delegation is untested. |
| Record counts | Zone `status.recordCount`; independently observed sets and their `spec.records` | A record set may contain multiple records. Unread inventory is never zero; counts from partial inventory are lower bounds. |
| Proxy programming | Aggregate `Programmed`, hostname `Available` | Programming and hostname claim evidence are separate reports. Aggregate status does not replace a missing hostname report. |
| TLS issuance | Hostname `CertificateReady`; aggregate `CertificatesReady` in object summary | Issuer report, not the certificate served to this client. |
| Connector | `Accepted`, `Ready`, nested capability conditions and `status.leaseRef` | Declared capabilities do not establish support. An unreported or unread lease is unknown. |
| Instance backend | `HTTPProxy.spec.rules[].backends[].instance.name` → **EndpointSlice** | This field does not name a compute Instance. Explicit endpoint readiness is reported independently; unreported readiness stays unknown. |
| Service backend | Exact same-namespace `networkService.name` | NetworkService conditions report available backend capacity; no traffic is inferred. |
| Endpoint backend | Configured URL | Reachability unassessed. |
| Network allocation | `status.ipam`, context/subnet/binding references | Reported allocation is distinct from requested configuration. |
| Public reachability | Not probed by this workspace | DNS resolution, delegation, served TLS and HTTP traffic remain unassessed. |

A current negative readiness condition yields **Attention needed** and a condition-based Issue. Stale observed generations are reconciling, not current failures. Unknown and absent conditions are not green; readiness requires the kind's principal conditions, rather than any single True condition. Inverted HTTPProxy `HostnamesInUse=True` is a problem. Pending controller reasons are quiet in Issues. Domain verification alternatives do not create false Issues after overall verification succeeds.

`coverage` is per kind: `full`, `partial`, `denied`, `notInstalled`, `syncing`, `uncached`, `error`, shared with CNPG. An empty readable inventory establishes absence only in its covered scope. A failed read is never an empty inventory. Workspace reads have a bounded aggregate request budget and bounded concurrency. Opening Home reads existing observations and does not watch every API.

## Resources, topology, Timeline and AI

Dedicated renderers cover DNSZone/RecordSet/Class, Domain, HTTPProxy, Connector/Class/Advertisement, Network/Context/Binding/Subnet/Claim/Service, compute Instance/Workload and ResourceManager Project/Organization. Renderers show actual schema fields, conditions at resource/hostname/record/capability scope and exact related-resource links. Generic Resources actions retain their existing authorization; no specialized Datum mutation is added.

The Resources topology includes configured DNSZone → DNSRecordSets, controller Domain references, inferred Domain/hostname associations, HTTPProxy → Connector/EndpointSlice/NetworkService/configured endpoint, and Network → Context/Binding/Subnet relationships. These are `configures` edges and are never injected into the Traffic graph. Declared but unobserved EndpointSlices are explicitly unknown. Cluster-scoped Project/class nodes are not introduced into shared topology.

Existing dynamic-resource callbacks feed Timeline with group-aware lifecycle/condition/spec observations. Runtime project connections use distinct cache, timeline and namespace-preference identities.

Existing REST AI and MCP `list_resources` / `get_resource` summaries carry reported readiness, hostnames, exact configured targets and condition failures. Domain verification challenges/contact data and Connector connection material are removed from every verbosity level. Compute sandbox container environment values follow the existing environment and inline-secret redaction rules. Configuration references survive redaction. Raw local resource details remain governed by the user's kubeconfig access.

## Project control-plane navigation

`POST /api/datum/projects/{name}/connect` is available only in local, auth-disabled Radar. It binds the reviewed context and Project UID, reads the Project using the original parent configuration, verifies the scoped Namespace API before tearing down caches, then switches. A post-teardown failure restores the prior connection. Subsequent switches reverify the Project identity and the scoped API.

The derived connection replaces a terminal organization/project control-plane path instead of appending another tenant path, retaining the server's preceding path prefix. It reloads the original parent kubeconfig's CA, TLS and auth/exec configuration. Radar never stores an exec-produced token, writes `~/.kube/config` or implements token refresh. Derived contexts exist only for this process and appear in the connection picker. A root namespace is not silently inherited into the project's isolated inventory.

The alternative is a copyable command:

```sh
datumctl auth update-kubeconfig --project <project-name>
```

This command changes kubeconfig only when you run it yourself.

## API and validation

`GET /api/datum/workspace` returns `installed`, active `context`, `namespaces`, per-kind `coverage`, `objects` and condition-based `issues`. Both new routes advertise `features.datumWorkspace`; frontend calls use the existing version-skew feature gate. These routes return 403 in shared/auth-enabled deployments. This experiment adds no default Helm or Cloud read grants.

The reproducible [fixture](../scripts/datum-demo/README.md) runs a real Milo API server and etcd, with immutable upstream CRD versions. All seeded controller status is prominently marked **synthetic**. API acceptance, discovery, cache behavior, UI rendering and tenant switching can be validated live without claiming DNS/network/controller reconciliation. See the implementation report for exact commands, screenshots and operator-attempt results. A real provider/cell/issuer environment remains required to validate public DNS, certificate issuance and traffic.

The workspace observes Lease and EndpointSlice inventories on demand through the shared dynamic cache. Home does not start these watches. Once opened, subsequent workspace refreshes reuse informer observations rather than issuing full API lists on every lease renewal.

On API-only control planes, discovery reports `on-demand`: the API registry is known and curated inventories are warmed, while other kinds are observed when opened. Diagnose retains this explicit coverage limit. Lease and EndpointSlice update noise stays out of Timeline and SSE; their workspace facts refresh on the bounded polling interval. Project-scoped integration settings store no fictitious kubeconfig source and cannot recreate a runtime context after restart. A Projects screen returns to Hostnames when the newly selected plane does not serve Projects.

### Reading the status board

Workspace tables show a concise value and a status dot using Radar's shared health tones. Hover a value for its exact object, API field and controller message, including the separate observations combined into a stage. Composed summaries use the same presentation; the Hostname chain also shows source labels inline. No combined cell claims a count of agreeing observations. Healthy rows have no top problem, and public reachability remains unassessed, stated once above the table with details on hover or expansion.

Synthetic status banners appear only when an object bears `radar.skyhook.io/synthetic-status: "true"`. Without that annotation there is no fixture banner. Collapsed sidebar group totals are unknown (`–`) when no positive subtotal is established; a positive subtotal with unread kinds is a lower bound (`≥N`), and a complete total is exact; opening a kind can establish its per-kind count. On-demand inventories are never inferred to contain zero objects. Bare `/resources` selects a listable discovered kind (core Pods when served, otherwise core Namespaces or another served kind). An explicitly requested unserved API says “Not served by this API server”; it is distinct from an access denial. API-only Home identifies its connection as an API server.
