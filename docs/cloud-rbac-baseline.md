# Radar Cloud default integration-read policy

Radar's connector collects resources with its service account. A Radar Cloud user's
Kubernetes permissions are separate: the default tiers bind viewer/member/owner
to `view`/`edit`/`admin`, which do not reliably include operator CRDs. Vendor role
aggregation may cover some types on some clusters. Without an explicit baseline,
an integration can be collected successfully but denied in an authorized diagnosis.

The chart adds two read-only ClusterRoles: `*-integration-read-namespaced` and
`*-integration-read-cluster`. Both grant only `get/list/watch` on exact reviewed
group/resource identities. Each enabled Radar Cloud tier gets its own bindings to
`radar:<tier>` and the existing compatibility group `cloud:<tier>`. Namespaced
resources are readable **across all namespaces** under the default bindings.
The grants apply to any Kubernetes identity carrying those groups, not just to a
particular UI or client.

## What becomes readable

The [reviewed policy table](../deploy/helm/radar/files/integration-read-baseline.yaml)
is the source the chart renders, not a list generated from runtime discovery.
It covers curated GitOps, routing/policy, autoscaling, observability, database,
backup, cluster lifecycle, policy-report and GPU/batch resources. Each tuple has
an explicit `grant`, `existing` or `withhold` decision, scope, collection flag and
rationale. Coverage tests require a decision for additions to the backend fallback,
cluster-scope and policy-report catalogs, curated frontend columns, and pinned GPU
inventory. Renderer-only kinds are not a coverage source; adding a renderer alone
does not grant access. Tests never turn collection rights into user grants automatically.

These are **full-object reads, not redacted projections**. Inline Helm values,
workload parameters, database connection configuration and Traefik plugin settings
are visible. For example, CNPG `Cluster.spec.externalClusters[].connectionParameters`
can include inline passwords. Put credentials in Secrets rather than ordinary
configuration, and use customer-managed RBAC if this default visibility is too broad.
This add-on grants no core Secrets, RBAC objects, token subresources, exec/proxy,
writes, or wildcard groups/resources. Existing role grants are not changed.
Trivy RBAC assessment reports remain readable as derived findings; this does not
grant access to the underlying Role or ClusterRole objects.

## Intentional exceptions

| Resources withheld by this add-on | Why / effect |
|---|---|
| External Secrets SecretStore and ClusterSecretStore | Provider authentication can contain plaintext credentials, such as Scaleway `secretKey.value`. ExternalSecret/ClusterExternalSecret remain readable; store inspection requires a separate customer grant. |
| KEDA TriggerAuthentication and ClusterTriggerAuthentication | `spec.hashiCorpVault.credential.token` may hold a Vault token directly. ScaledObject/ScaledJob remain readable. |
| Kyverno UpdateRequest | Admission context may embed the actual triggering object, including a Secret. Queued-policy-work remains denied without another grant. |
| CAPI KubeadmConfig, KubeadmConfigTemplate, KubeadmControlPlane, KubeadmControlPlaneTemplate | Bootstrap configuration can contain inline files and credentials; the renderer-only control-plane template is also withheld pending credential-field review. |
| CAPI AzureMachine and AzureMachineTemplate | Extension `protectedSettings` may contain credentials. Azure-side protection does not encrypt the copy in the Kubernetes object. Managed-cluster/control-plane/pool resources remain unchanged. |
| CAPI GCPMachine and GCPMachineTemplate | Root/additional disk configuration may contain `suppliedKey.rawKey`: base64-encoded encryption keys, not Secret references. Managed-cluster/control-plane/pool resources remain unchanged. |
| Argo AnalysisRun, AnalysisTemplate and ClusterAnalysisTemplate | Prometheus OAuth configuration accepts a literal `clientSecret`. Rollout/Experiment and Argo CD resources remain readable. |
| Argo Workflow, WorkflowTemplate, ClusterWorkflowTemplate and CronWorkflow | Artifact OSS configuration accepts a literal `securityToken`; Workflow status can also retain captured output/parameters. |
| Crossplane Kubernetes Object | Desired/observed manifests may contain arbitrary Secret payloads. |
| Crossplane Kubernetes/Helm ProviderConfig and Helm Release | Pending provider-specific credential/connection-details review; these are deliberate coverage limits, not claims that every instance is sensitive. |
| Trivy ExposedSecretReport | Secret-detection excerpts; examples are masked, but no blanket masking guarantee is assumed. Other curated report kinds remain readable. |
| Legacy `traefik.containo.us/serverstransporttcps` | Exact legacy-group schema remains unverified. The reviewed `traefik.io/serverstransporttcps` tuple remains readable. |

These exclusions are not a secret-free guarantee for the remaining grants.
Argo CD Applications can retain local-sync manifests containing Secrets;
cert-manager Certificates accept literal keystore passwords; Ray cluster specs
accept literal Redis passwords. These grants remain unchanged pending separate
policy decisions. Flexible templates, patches, connection parameters and report
messages can also contain sensitive values; omitting core Secrets does not redact
those fields.

The table cites the source for each exception. Arbitrary Crossplane MR/XR/Claim
groups, collector-only integrations and custom `rbac.additionalCrdGroups` are not
automatically granted. Supply explicit customer roles for additional access.
`withhold` means this add-on does not grant the tuple; Kubernetes has no deny rule
here. Other roles, vendor aggregation or custom bindings may still authorize it.

## Controls and compatibility

| Setting | Effect on the new baseline |
|---|---|
| `cloud.enabled=false`, `rbac.create=false`, or `cloud.defaultRbac.create=false` | No new roles/bindings |
| `cloud.defaultRbac.<tier>=false` | No integration bindings for that tier |
| `cloud.defaultRbac.integrationRead.<tier>=false` | No new integration bindings for that tier; does not remove existing add-ons |
| `cloud.defaultRbac.clusterScopedRead.<tier>=false` | No new cluster-scoped integration binding; namespaced baseline remains |
| `rbac.crdGroups.<integration>=false` | Excludes that family's new grants unless `crdGroups.all=true` |
| `rbac.crdGroups.all=true` | Enables the finite reviewed set, never wildcard caller access |
| `rbac.additionalCrdGroups` | Collection configuration only; does not enlarge this user baseline |
| Custom `viewerClusterRole` / `memberClusterRole` / `ownerClusterRole` | Does not disable the independent integration bindings |

Existing cluster-read grants stay intact, including their historical switches and
Karpenter wildcard. The old Karpenter/Cilium/Prometheus rules do not follow `all`;
the new baseline does, for its named tuples only. Namespaced PrometheusRules and
CiliumNetworkPolicies are intentionally also in the new namespaced role, so they
do not depend on enabling cluster infrastructure reads. Turning off the new
add-on does not revoke those old grants. Member/owner write permissions are unchanged.

Enterprise customers who want only their own group bindings can set
`cloud.defaultRbac.create=false`. The Hub can still forward IdP groups as
`radar:idp:<group>`; bind these to Roles in the intended namespaces. Adding a
namespace RoleBinding does not restrict an existing cluster-wide default binding,
so disable the defaults first. Product organization roles and saved-investigation
sharing are separate concerns; this chart change does not redefine them.

## Rollout and verification

The baseline is default-on for enabled default tiers. Upgrading an installed chart
therefore expands deliberate read access, including inline configuration. Missing
`integrationRead` maps/keys on `helm upgrade --reuse-values` follow the declared
default; explicit `false` is preserved. Review/set opt-outs before the upgrade.
Upgrading only the binary or using Radar Cloud self-upgrade does not install these RBAC
objects. No cluster configuration sync or automatic rollout is introduced.

Test with `go test ./internal/k8s -run TestIntegrationRead`, chart lint/unittests,
and the shared UI's `curated-column-ownership.test.ts`. The tests cover exact
permissions, scope, caller bindings, collection and tier gates, old value trees,
intentional exceptions, and unchanged existing manifests. A live `can-i` check
must use the caller's groups, not the collector service account.

This PR fixes shipped grants, **not every response-authorization path**. It does
not claim that omission from the baseline prevents disclosure through a separate
cached-response authorization bug. Runtime enforcement and its upgrade impact
remain independent work; tests for custom denied users must not rely on these defaults.
