import { clsx } from 'clsx'
import { SEVERITY_BADGE } from '../../utils/badge-colors'
import {
  getPodStatus,
  getWorkloadStatus,
  getJobStatus,
  getCronJobStatus,
  getHPAStatus,
  getServiceStatus,
  getNodeStatus,
  getPVCStatus,
  getRolloutStatus,
  getWorkflowStatus,
  getCronWorkflowStatus,
  getCertificateStatus,
  getPVStatus,
  getClusterIssuerStatus,
  getIssuerStatus,
  getOrderState,
  getChallengeState,
  getCertificateRequestStatus,
  getGatewayStatus,
  getGatewayClassStatus,
  getRouteStatus,
  getSealedSecretStatus,
  getPDBStatus,
  getGitRepositoryStatus,
  getOCIRepositoryStatus,
  getHelmRepositoryStatus,
  getKustomizationStatus,
  getFluxHelmReleaseStatus,
  getFluxAlertStatus,
  getArgoApplicationStatus,
  getVulnerabilityReportStatus,
  getConfigAuditReportStatus,
  getExposedSecretReportStatus,
  getRbacAssessmentReportStatus,
  getClusterComplianceReportStatus,
  getSbomReportStatus,
} from '../resources/resource-utils'
import {
  LabelsSection,
  AnnotationsSection,
  MetadataSection,
  EventsSection,
  RelatedResourcesSection,
  ExternalLinksSection,
  AppInfoSection,
} from '../ui/drawer-components'
import { getNodePoolStatus, getNodeClaimStatus, getEC2NodeClassStatus } from '../resources/resource-utils-karpenter'
import { getScaledObjectStatus, getScaledJobStatus } from '../resources/resource-utils-keda'
import { getServiceMonitorStatus, getPrometheusRuleStatus, getPodMonitorStatus } from '../resources/resource-utils-prometheus'
import { getPolicyReportStatus, getKyvernoPolicyStatus } from '../resources/resource-utils-kyverno'
import { getResourceClaimStatus, getResourceClaimTemplateStatus, getDeviceClassStatus, getResourceSliceStatus } from '../resources/resource-utils-dra'
import { getNvidiaClusterPolicyStatus, getNvidiaDriverStatus } from '../resources/resource-utils-nvidia'
import { getBackupStatus, getRestoreStatus, getScheduleStatus, getBSLStatus } from '../resources/resource-utils-velero'
import {
  getVirtualServiceStatus,
  getDestinationRuleStatus,
  getIstioGatewayStatus,
  getServiceEntryStatus,
  getPeerAuthenticationStatus,
  getAuthorizationPolicyStatus,
} from '../resources/resource-utils-istio'
import { getCNPGClusterStatus, getCNPGBackupStatus, getCNPGScheduledBackupStatus, getCNPGPoolerStatus } from '../resources/resource-utils-cnpg'
import { getExternalSecretStatus, getClusterExternalSecretStatus, getSecretStoreStatus, getClusterSecretStoreStatus } from '../resources/resource-utils-eso'
import {
  getKnativeConditionStatus,
  getRevisionStatus,
} from '../resources/resource-utils-knative'
import { getHTTPProxyStatus } from '../resources/resource-utils-contour'
import { getClusterStatus as getCAPIClusterStatus, getMachineStatus, getMachineDeploymentStatus, getMachineSetStatus, getMachinePoolStatus, getKCPStatus, getClusterClassStatus, getMachineHealthCheckStatus } from '../resources/resource-utils-capi'
import { getAWSMCPStatus, getAWSMMPStatus, getAWSMachineStatus, getAWSManagedClusterStatus } from '../resources/resource-utils-aws-capi'
import { getGCPMCPStatus, getGCPMMPStatus, getGCPMachineStatus, getGCPManagedClusterStatus } from '../resources/resource-utils-gcp-capi'
import { getAzureMCPStatus, getAzureMMPStatus, getAzureMachineStatus, getAzureManagedClusterStatus } from '../resources/resource-utils-azure-capi'
import {
  PodRenderer,
  WorkloadRenderer,
  ReplicaSetRenderer,
  ServiceRenderer,
  IngressRenderer,
  ConfigMapRenderer,
  SecretRenderer,
  JobRenderer,
  CronJobRenderer,
  CronWorkflowRenderer,
  HPARenderer,
  NodeRenderer,
  PVCRenderer,
  RolloutRenderer,
  CertificateRenderer,
  WorkflowRenderer,
  PersistentVolumeRenderer,
  StorageClassRenderer,
  CertificateRequestRenderer,
  ClusterIssuerRenderer,
  IssuerRenderer,
  OrderRenderer,
  ChallengeRenderer,
  GatewayRenderer,
  GatewayClassRenderer,
  HTTPRouteRenderer,
  GRPCRouteRenderer,
  SimpleRouteRenderer,
  SealedSecretRenderer,
  WorkflowTemplateRenderer,
  NetworkPolicyRenderer,
  CiliumNetworkPolicyRenderer,
  ClusterNetworkPolicyRenderer,
  PodDisruptionBudgetRenderer,
  ServiceAccountRenderer,
  NamespaceRenderer,
  RoleRenderer,
  RoleBindingRenderer,
  WebhookConfigRenderer,
  EventRenderer,
  EndpointSliceRenderer,
  GenericRenderer,
  GitRepositoryRenderer,
  OCIRepositoryRenderer,
  HelmRepositoryRenderer,
  KustomizationRenderer,
  FluxHelmReleaseRenderer,
  AlertRenderer,
  ArgoApplicationRenderer,
  VulnerabilityReportRenderer,
  ConfigAuditReportRenderer,
  ExposedSecretReportRenderer,
  ClusterComplianceReportRenderer,
  SbomReportRenderer,
  KarpenterNodePoolRenderer,
  KarpenterNodeClaimRenderer,
  KarpenterEC2NodeClassRenderer,
  KedaScaledObjectRenderer,
  KedaScaledJobRenderer,
  KedaTriggerAuthRenderer,
  VPARenderer,
  ServiceMonitorRenderer,
  PrometheusRuleRenderer,
  PodMonitorRenderer,
  PolicyReportRenderer,
  KyvernoPolicyRenderer,
  VeleroBackupRenderer,
  VeleroRestoreRenderer,
  VeleroScheduleRenderer,
  VeleroBSLRenderer,
  VeleroVSLRenderer,
  CNPGClusterRenderer,
  CNPGBackupRenderer,
  CNPGScheduledBackupRenderer,
  CNPGPoolerRenderer,
  ExternalSecretRenderer,
  ClusterExternalSecretRenderer,
  SecretStoreRenderer,
  IstioVirtualServiceRenderer,
  IstioDestinationRuleRenderer,
  IstioGatewayRenderer,
  IstioServiceEntryRenderer,
  IstioPeerAuthenticationRenderer,
  IstioAuthorizationPolicyRenderer,
  KnativeServiceRenderer,
  KnativeRevisionRenderer,
  KnativeRouteRenderer,
  KnativeConfigurationRenderer,
  KnativeIngressRenderer,
  KnativeCertificateRenderer,
  ServerlessServiceRenderer,
  BrokerRenderer,
  TriggerRenderer,
  EventTypeRenderer,
  ChannelRenderer,
  InMemoryChannelRenderer,
  SubscriptionRenderer,
  PingSourceRenderer,
  ApiServerSourceRenderer,
  ContainerSourceRenderer,
  SinkBindingRenderer,
  SequenceRenderer,
  ParallelRenderer,
  DomainMappingRenderer,
  IngressClassRenderer,
  PriorityClassRenderer,
  RuntimeClassRenderer,
  LeaseRenderer,
  TraefikIngressRouteRenderer,
  TraefikMiddlewareRenderer,
  TraefikServiceRenderer,
  TraefikTLSOptionRenderer,
  TraefikServersTransportRenderer,
  ContourHTTPProxyRenderer,
  CAPIClusterRenderer,
  CAPIMachineRenderer,
  CAPIMachineDeploymentRenderer,
  CAPIKubeadmControlPlaneRenderer,
  CAPIMachineSetRenderer,
  CAPIMachinePoolRenderer,
  CAPIClusterClassRenderer,
  CAPIMachineHealthCheckRenderer,
  CAPIMachineDrainRuleRenderer,
  CAPIKubeadmConfigRenderer,
  AWSManagedControlPlaneRenderer,
  AWSManagedMachinePoolRenderer,
  AWSMachineRenderer,
  AWSMachineTemplateRenderer,
  AWSManagedClusterRenderer,
  GCPManagedControlPlaneRenderer,
  GCPManagedMachinePoolRenderer,
  GCPMachineRenderer,
  AzureManagedControlPlaneRenderer,
  AzureManagedMachinePoolRenderer,
  AzureMachineRenderer,
  ManagedResourceRenderer,
  CompositeRenderer,
  CrossplanePackageRenderer,
  CrossplaneProviderConfigRenderer,
  CompositionRenderer,
  CompositionRevisionRenderer,
  XRDRenderer,
  ResourceClaimRenderer,
  ResourceClaimTemplateRenderer,
  DeviceClassRenderer,
  ResourceSliceRenderer,
  NvidiaClusterPolicyRenderer,
  NvidiaDriverRenderer,
} from '../resources/renderers'
import type { ComposedRefStatus } from '../resources/renderers/CompositeRenderer'
import {
  getCrossplaneStatus,
  getProviderStatus,
  getProviderConfigStatus,
  isManagedResource,
  isComposite,
  isClaim,
} from '../resources/resource-utils-crossplane'
import type { SelectedResource, Relationships, ResourceRef, SecretCertificateInfo, ResolvedEnvFrom, TimelineEvent, HPADiagnosis } from '../../types'
import type { CopyHandler } from '../ui/drawer-components'
import { AlertBanner } from '../ui/drawer-components'
import { replicaScalers } from '../../utils/replica-scalers'
import type { ScalerDiagnosis } from '../resources/renderers/WorkloadRenderer'

/**
 * Override map letting each platform consumer swap in its own renderer components.
 * Each override receives only the props that ResourceRendererDispatch passes at its
 * call site — a subset of the base renderer's full props. The override is responsible
 * for wiring any additional behavior (metrics, exec, port-forward, scale, etc.) internally.
 *
 * When an override is not provided, the base (shared) renderer is used.
 */
export interface RendererOverrides {
  PodRenderer?: React.ComponentType<{
    data: any; onCopy: CopyHandler; copied: string | null
    onNavigate?: (ref: ResourceRef) => void
    onOpenLogs?: (podName: string, containerName: string) => void
    resolvedEnvFrom?: ResolvedEnvFrom
  }>
  NodeRenderer?: React.ComponentType<{
    data: any; relationships?: Relationships
  }>
  ServiceRenderer?: React.ComponentType<{
    data: any; onCopy: CopyHandler; copied: string | null
    onNavigate?: (ref: ResourceRef) => void
  }>
  WorkloadRenderer?: React.ComponentType<{
    kind: string; data: any
    onNavigate?: (ref: ResourceRef) => void
    relationships?: Relationships
    scaleBlockedBy?: ResourceRef[]
    scalerDiagnostics?: ScalerDiagnosis[]
  }>
  // Optional override for Crossplane Composite / Claim — host wraps the
  // package renderer to fan out per-composed-ref status fetches via React Query.
  CompositeRenderer?: React.ComponentType<{
    data: any
    onNavigate?: (ref: ResourceRef) => void
    composedRefStatuses?: Map<string, ComposedRefStatus>
  }>
  // ServiceAccount reverse-lookup: the host fetches /api/rbac/subject/... and
  // feeds the result into the base renderer via this wrapper.
  ServiceAccountRenderer?: React.ComponentType<{
    data: any
    onNavigate?: (ref: ResourceRef) => void
  }>
  // Role / ClusterRole reverse-lookup: host fetches /api/rbac/role/... so the
  // detail page can show "who is bound to this role".
  RoleRenderer?: React.ComponentType<{
    data: any
    onNavigate?: (ref: ResourceRef) => void
  }>
  // RoleBinding inline rules preview: host fetches the referenced Role/
  // ClusterRole's rules so the binding view can show what's granted without
  // a navigation step.
  RoleBindingRenderer?: React.ComponentType<{
    data: any
    onNavigate?: (ref: ResourceRef) => void
  }>
  // Namespace RBAC summary: host fetches /api/rbac/namespace/{ns} so the
  // namespace page can show bindings configured here without falling
  // through to GenericRenderer.
  NamespaceRenderer?: React.ComponentType<{
    data: any
    onNavigate?: (ref: ResourceRef) => void
  }>
  // HPA: host wraps the base renderer to add Prometheus-backed replicas /
  // metric charts below the static spec data.
  HPARenderer?: React.ComponentType<{
    data: any
    onNavigate?: (ref: ResourceRef) => void
    hpaDiagnosis?: HPADiagnosis
  }>
  // PVC: host wraps the base renderer to add a kubelet-derived usage gauge
  // when Prometheus is scraping kubelet endpoints.
  PVCRenderer?: React.ComponentType<{
    data: any
    onNavigate?: (ref: ResourceRef) => void
  }>
}

// Known resource types with specific renderers (module-level to avoid re-allocation)
const KNOWN_KINDS = new Set([
  'pods', 'deployments', 'statefulsets', 'daemonsets', 'replicasets',
  'services', 'endpointslices', 'ingresses', 'configmaps', 'secrets', 'jobs', 'cronjobs', 'cronworkflows',
  'hpas', 'horizontalpodautoscalers', 'nodes', 'persistentvolumeclaims',
  'rollouts', 'certificates', 'workflows', 'persistentvolumes',
  'storageclasses', 'certificaterequests', 'clusterissuers', 'issuers',
  'orders', 'challenges',
  'gateways', 'gatewayclasses', 'httproutes', 'grpcroutes', 'tcproutes', 'tlsroutes', 'sealedsecrets', 'workflowtemplates', 'clusterworkflowtemplates',
  'networkpolicies', 'networkpolicy',
  'ciliumnetworkpolicies', 'ciliumnetworkpolicy', 'ciliumclusterwidenetworkpolicies', 'ciliumclusterwidenetworkpolicy',
  'clusternetworkpolicies', 'clusternetworkpolicy',
  'poddisruptionbudgets', 'serviceaccounts', 'namespaces',
  'roles', 'clusterroles', 'rolebindings', 'clusterrolebindings',
  'events', 'gitrepositories', 'ocirepositories', 'helmrepositories',
  'kustomizations', 'helmreleases', 'alerts', 'applications',
  'nodepools', 'nodeclaims', 'ec2nodeclasses', 'scaledobjects', 'scaledjobs',
  'triggerauthentications', 'clustertriggerauthentications',
  'servicemonitors', 'prometheusrules', 'podmonitors',
  'policyreports', 'clusterpolicyreports', 'kyvernopolicies', 'clusterpolicies',
  'resourceclaims', 'resourceclaimtemplates', 'deviceclasses', 'resourceslices',
  'nvidiadrivers',
  'vulnerabilityreports', 'configauditreports', 'exposedsecretreports',
  'rbacassessmentreports', 'clusterrbacassessmentreports',
  'clustercompliancereports', 'sbomreports', 'clustersbomreports',
  'infraassessmentreports', 'clusterinfraassessmentreports',
  'verticalpodautoscalers',
  'backups', 'restores', 'schedules', 'backupstoragelocations', 'volumesnapshotlocations',
  'externalsecrets', 'clusterexternalsecrets', 'secretstores', 'clustersecretstores',
  'clusters', 'scheduledbackups', 'poolers',
  'virtualservices', 'destinationrules', 'serviceentries',
  'peerauthentications', 'authorizationpolicies',
  'mutatingwebhookconfigurations', 'validatingwebhookconfigurations',
  'ingressclasses', 'priorityclasses', 'runtimeclasses', 'leases',
  'knativeservices', 'knativeconfigurations', 'knativerevisions', 'knativeroutes',
  'brokers', 'triggers', 'eventtypes', 'pingsources', 'apiserversources', 'containersources', 'sinkbindings',
  'channels', 'inmemorychannels', 'subscriptions', 'sequences', 'parallels',
  'knativeingresses', 'knativecertificates', 'serverlessservices', 'domainmappings',
  'ingressroutes', 'ingressroutetcps', 'ingressrouteudps',
  'middlewares', 'middlewaretcps', 'traefikservices',
  'serverstransports', 'serverstransporttcps', 'tlsoptions',
  'httpproxies',
  'machinedeployments', 'machines', 'machinesets', 'machinepools',
  'kubeadmcontrolplanes', 'clusterclasses', 'machinehealthchecks',
  'machinedrainrules', 'kubeadmconfigs', 'kubeadmconfigtemplates',
  'kubeadmcontrolplanetemplates',
  // AWS CAPI Infrastructure Provider
  'awsmanagedcontrolplanes', 'awsmanagedmachinepools', 'awsmachines',
  'awsmachinetemplates', 'awsmanagedclusters',
  // GCP CAPI Infrastructure Provider
  'gcpmanagedcontrolplanes', 'gcpmanagedmachinepools', 'gcpmachines',
  'gcpmachinetemplates', 'gcpmanagedclusters',
  // Azure CAPI Infrastructure Provider
  'azuremanagedcontrolplanes', 'azuremanagedmachinepools', 'azuremachines',
  'azuremachinetemplates', 'azuremanagedclusters',
  // Crossplane core (Managed Resources, Composites, and Claims are detected
  // dynamically by spec shape — their plurals are unbounded so they're handled
  // via fall-through, not enumerated in KNOWN_KINDS).
  'providers', 'providerconfigs',
  'compositeresourcedefinitions', 'compositions', 'compositionrevisions',
  'functions', 'configurations',
])

// ============================================================================
// RESOURCE CONTENT - Delegates to specific renderers
// ============================================================================

interface ResourceRendererDispatchProps {
  resource: SelectedResource
  data: any
  relationships?: Relationships
  certificateInfo?: SecretCertificateInfo
  hpaDiagnosis?: HPADiagnosis
  scalerDiagnostics?: ScalerDiagnosis[]
  onCopy: (text: string, key: string) => void
  copied: string | null
  onNavigate?: (ref: ResourceRef) => void
  onSaveSecretValue?: (yaml: string) => Promise<void>
  isSavingSecret?: boolean
  /** Set to false to skip common trailing sections (events, labels, annotations, metadata, metrics, related) */
  showCommonSections?: boolean
  /** Set to false to skip Prometheus charts (useful when a parent view has a dedicated Metrics tab) */
  showMetrics?: boolean
  /** When provided, container-level Logs buttons call this instead of opening the dock */
  onOpenLogs?: (podName: string, containerName: string) => void
  /** Resolved ConfigMap/Secret data for envFrom expansion in PodRenderer */
  resolvedEnvFrom?: ResolvedEnvFrom
  /** Platform-specific renderer overrides (e.g. with hooks for metrics, exec, port-forward) */
  rendererOverrides?: RendererOverrides
  /** Optional hint shown in the Events section (e.g. link to Timeline tab) */
  eventsHint?: React.ReactNode
  /** When provided, sidebar sections (related resources, events, labels, annotations, metadata) are passed to this render prop instead of being rendered inline */
  renderSidebar?: (sections: React.ReactNode) => React.ReactNode
  /** Additional overview content that belongs in the main content column after the resource renderer sections. */
  mainFooter?: React.ReactNode
  /** K8s events for the focused resource — always shown (no toggle hides them)
   *  so resource history can't go missing. */
  events?: TimelineEvent[]
  /** Whether events are still loading */
  eventsLoading?: boolean
  /** Resource update events (informer/historical diffs) — hidden behind a
   *  toggle in the Recent Events section because they can be very high-volume
   *  for a flapping resource. */
  updates?: TimelineEvent[]
  /** Errors from the events / updates queries — surfaced inline in the
   *  Recent Events section so a partial failure doesn't render as empty. */
  eventsError?: Error | null
  updatesError?: Error | null
  /** Render prop for Prometheus metrics charts — injected by the platform wrapper */
  renderMetrics?: (props: { kind: string; namespace: string; name: string }) => React.ReactNode
}

export function ResourceRendererDispatch({
  resource,
  data,
  relationships,
  certificateInfo,
  hpaDiagnosis,
  scalerDiagnostics,
  onCopy,
  copied,
  onNavigate,
  onSaveSecretValue,
  isSavingSecret,
  showCommonSections = true,
  showMetrics = true,
  onOpenLogs,
  eventsHint,
  renderSidebar,
  mainFooter,
  events,
  eventsLoading,
  updates,
  eventsError,
  updatesError,
  renderMetrics,
  resolvedEnvFrom,
  rendererOverrides,
}: ResourceRendererDispatchProps) {
  const kind = resource.kind.toLowerCase()

  // Crossplane Managed Resources / Composites / Claims are detected by spec
  // shape because their plurals are unbounded (one CRD kind per provider
  // service). These flags suppress the GenericRenderer fall-through and route
  // to the right Crossplane renderer below.
  const isCrossplaneMR = isManagedResource(data)
  const isCrossplaneClaim = !isCrossplaneMR && isClaim(data)
  const isCrossplaneXR = !isCrossplaneMR && !isCrossplaneClaim && isComposite(data)

  // Crossplane plurals that collide with foreign CRDs — `configurations`
  // overlaps Knative serving.knative.dev/Configuration, `functions` could
  // collide with OpenFaaS, `compositions` is in this list because its
  // render line is apiVersion-gated. We add these to KNOWN_KINDS so the
  // Crossplane renderer wins on apiVersion match, but a foreign CR with
  // the same plural needs to fall through to GenericRenderer — otherwise
  // it renders blank (no Crossplane match + isKnownKind suppresses
  // generic). Only kinds whose render lines are apiVersion-gated belong
  // here; `providerconfigs`/`compositionrevisions`/`compositeresource-
  // definitions` are Crossplane-specific plurals with no realistic
  // collision and unguarded render lines, so including them would risk
  // double-render for foreign CRDs we'll never see.
  const isCollisionGatedKind =
    kind === 'providers' || kind === 'functions' || kind === 'configurations' || kind === 'compositions'
  const crossplaneApiVersionMatched = isCollisionGatedKind && (
    data?.apiVersion?.startsWith('pkg.crossplane.io/')
    || data?.apiVersion?.startsWith('apiextensions.crossplane.io/')
  )
  const crossplaneCollisionFallthrough = isCollisionGatedKind && !crossplaneApiVersionMatched

  const isKnownKind = KNOWN_KINDS.has(kind) || isCrossplaneMR || isCrossplaneClaim || isCrossplaneXR

  const PodComp = rendererOverrides?.PodRenderer ?? PodRenderer
  const WorkloadComp = rendererOverrides?.WorkloadRenderer ?? WorkloadRenderer
  const NodeComp = rendererOverrides?.NodeRenderer ?? NodeRenderer
  const ServiceComp = rendererOverrides?.ServiceRenderer ?? ServiceRenderer
  const CompositeComp = rendererOverrides?.CompositeRenderer ?? CompositeRenderer
  const ServiceAccountComp = rendererOverrides?.ServiceAccountRenderer ?? ServiceAccountRenderer
  const RoleComp = rendererOverrides?.RoleRenderer ?? RoleRenderer
  const RoleBindingComp = rendererOverrides?.RoleBindingRenderer ?? RoleBindingRenderer
  const NamespaceComp = rendererOverrides?.NamespaceRenderer ?? NamespaceRenderer
  const HPAComp = rendererOverrides?.HPARenderer ?? HPARenderer
  const PVCComp = rendererOverrides?.PVCRenderer ?? PVCRenderer
  const scaleBlockedBy = replicaScalers(relationships?.scalers)

  const sidebarContent = showCommonSections && (
    <>
      <RelatedResourcesSection relationships={relationships} onNavigate={onNavigate} />
      {kind !== 'events' && <EventsSection events={events || []} updates={updates || []} isLoading={eventsLoading ?? false} eventsError={eventsError ?? null} updatesError={updatesError ?? null} hint={eventsHint} />}
      <LabelsSection data={data} />
      <AnnotationsSection data={data} />
      <MetadataSection data={data} />
    </>
  )

  return (
    <div className={renderSidebar ? 'lg:flex' : ''}>
      <div className={clsx('p-4 space-y-4', renderSidebar && 'lg:flex-1 lg:min-w-0')}>
        {/* Kind-specific content - delegates to modular renderers */}
        {kind === 'pods' && <PodComp data={data} onCopy={onCopy} copied={copied} onNavigate={onNavigate} onOpenLogs={onOpenLogs} resolvedEnvFrom={resolvedEnvFrom} />}
        {['deployments', 'statefulsets', 'daemonsets'].includes(kind) && (
          <WorkloadComp
            kind={kind}
            data={data}
            onNavigate={onNavigate}
            relationships={relationships}
            scaleBlockedBy={scaleBlockedBy}
            scalerDiagnostics={scalerDiagnostics}
          />
        )}
        {kind === 'replicasets' && <ReplicaSetRenderer data={data} />}
        {kind === 'services' && !data?.apiVersion?.includes('serving.knative.dev') && <ServiceComp data={data} onCopy={onCopy} copied={copied} onNavigate={onNavigate} />}
        {kind === 'endpointslices' && <EndpointSliceRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'ingresses' && !data?.apiVersion?.includes('networking.internal.knative.dev') && <IngressRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'configmaps' && <ConfigMapRenderer data={data} />}
        {kind === 'secrets' && <SecretRenderer data={data} certificateInfo={certificateInfo} resourceData={data} onSaveSecretValue={onSaveSecretValue} isSaving={isSavingSecret} />}
        {kind === 'jobs' && <JobRenderer data={data} />}
        {kind === 'cronjobs' && <CronJobRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'cronworkflows' && <CronWorkflowRenderer data={data} onNavigate={onNavigate} />}
        {(kind === 'hpas' || kind === 'horizontalpodautoscalers') && <HPAComp data={data} onNavigate={onNavigate} hpaDiagnosis={hpaDiagnosis} />}
        {kind === 'nodes' && <NodeComp data={data} relationships={relationships} />}
        {kind === 'persistentvolumeclaims' && <PVCComp data={data} onNavigate={onNavigate} />}
        {kind === 'rollouts' && <RolloutRenderer data={data} />}
        {kind === 'certificates' && !data?.apiVersion?.includes('networking.internal.knative.dev') && <CertificateRenderer data={data} />}
        {kind === 'workflows' && <WorkflowRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'persistentvolumes' && <PersistentVolumeRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'storageclasses' && <StorageClassRenderer data={data} />}
        {kind === 'certificaterequests' && <CertificateRequestRenderer data={data} />}
        {kind === 'clusterissuers' && <ClusterIssuerRenderer data={data} />}
        {kind === 'issuers' && <IssuerRenderer data={data} />}
        {kind === 'orders' && <OrderRenderer data={data} />}
        {kind === 'challenges' && <ChallengeRenderer data={data} />}
        {kind === 'gateways' && (data.apiVersion?.includes('networking.istio.io') ? <IstioGatewayRenderer data={data} /> : <GatewayRenderer data={data} onNavigate={onNavigate} />)}
        {kind === 'gatewayclasses' && <GatewayClassRenderer data={data} />}
        {kind === 'httproutes' && <HTTPRouteRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'grpcroutes' && <GRPCRouteRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'tcproutes' && <SimpleRouteRenderer data={data} kind="TCPRoute" onNavigate={onNavigate} />}
        {kind === 'tlsroutes' && <SimpleRouteRenderer data={data} kind="TLSRoute" onNavigate={onNavigate} />}
        {kind === 'sealedsecrets' && <SealedSecretRenderer data={data} onNavigate={onNavigate} />}
        {(kind === 'workflowtemplates' || kind === 'clusterworkflowtemplates') && <WorkflowTemplateRenderer data={data} />}
        {(kind === 'networkpolicies' || kind === 'networkpolicy') && <NetworkPolicyRenderer data={data} />}
        {(kind === 'ciliumnetworkpolicies' || kind === 'ciliumnetworkpolicy' || kind === 'ciliumclusterwidenetworkpolicies' || kind === 'ciliumclusterwidenetworkpolicy') && <CiliumNetworkPolicyRenderer data={data} />}
        {(kind === 'clusternetworkpolicies' || kind === 'clusternetworkpolicy') && <ClusterNetworkPolicyRenderer data={data} />}
        {kind === 'poddisruptionbudgets' && <PodDisruptionBudgetRenderer data={data} />}
        {kind === 'serviceaccounts' && <ServiceAccountComp data={data} onNavigate={onNavigate} />}
        {kind === 'namespaces' && <NamespaceComp data={data} onNavigate={onNavigate} />}
        {(kind === 'roles' || kind === 'clusterroles') && <RoleComp data={data} onNavigate={onNavigate} />}
        {(kind === 'rolebindings' || kind === 'clusterrolebindings') && <RoleBindingComp data={data} onNavigate={onNavigate} />}
        {kind === 'events' && <EventRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'gitrepositories' && <GitRepositoryRenderer data={data} />}
        {kind === 'ocirepositories' && <OCIRepositoryRenderer data={data} />}
        {kind === 'helmrepositories' && <HelmRepositoryRenderer data={data} />}
        {kind === 'kustomizations' && <KustomizationRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'helmreleases' && <FluxHelmReleaseRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'alerts' && <AlertRenderer data={data} />}
        {kind === 'applications' && <ArgoApplicationRenderer data={data} />}
        {kind === 'nodepools' && <KarpenterNodePoolRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'nodeclaims' && <KarpenterNodeClaimRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'ec2nodeclasses' && <KarpenterEC2NodeClassRenderer data={data} />}
        {kind === 'scaledobjects' && <KedaScaledObjectRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'scaledjobs' && <KedaScaledJobRenderer data={data} />}
        {(kind === 'triggerauthentications' || kind === 'clustertriggerauthentications') && <KedaTriggerAuthRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'vulnerabilityreports' && <VulnerabilityReportRenderer data={data} />}
        {kind === 'configauditreports' && <ConfigAuditReportRenderer data={data} />}
        {kind === 'exposedsecretreports' && <ExposedSecretReportRenderer data={data} />}
        {(kind === 'rbacassessmentreports' || kind === 'clusterrbacassessmentreports' || kind === 'infraassessmentreports' || kind === 'clusterinfraassessmentreports') && <ConfigAuditReportRenderer data={data} />}
        {kind === 'clustercompliancereports' && <ClusterComplianceReportRenderer data={data} />}
        {(kind === 'sbomreports' || kind === 'clustersbomreports') && <SbomReportRenderer data={data} />}
        {kind === 'verticalpodautoscalers' && <VPARenderer data={data} onNavigate={onNavigate} />}
        {kind === 'servicemonitors' && <ServiceMonitorRenderer data={data} />}
        {kind === 'prometheusrules' && <PrometheusRuleRenderer data={data} />}
        {kind === 'podmonitors' && <PodMonitorRenderer data={data} />}
        {(kind === 'policyreports' || kind === 'clusterpolicyreports') && <PolicyReportRenderer data={data} />}
        {(kind === 'kyvernopolicies' || (kind === 'clusterpolicies' && !data?.apiVersion?.startsWith('nvidia.com/'))) && <KyvernoPolicyRenderer data={data} />}
        {(kind === 'clusterpolicies' && data?.apiVersion?.startsWith('nvidia.com/')) && <NvidiaClusterPolicyRenderer data={data} />}
        {kind === 'nvidiadrivers' && <NvidiaDriverRenderer data={data} />}
        {/* DRA (resource.k8s.io) */}
        {kind === 'resourceclaims' && <ResourceClaimRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'resourceclaimtemplates' && <ResourceClaimTemplateRenderer data={data} />}
        {kind === 'deviceclasses' && <DeviceClassRenderer data={data} />}
        {kind === 'resourceslices' && <ResourceSliceRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'backups' && data.apiVersion?.includes('cnpg.io') && <CNPGBackupRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'backups' && !data.apiVersion?.includes('cnpg.io') && <VeleroBackupRenderer data={data} />}
        {kind === 'restores' && <VeleroRestoreRenderer data={data} />}
        {kind === 'schedules' && <VeleroScheduleRenderer data={data} />}
        {kind === 'backupstoragelocations' && <VeleroBSLRenderer data={data} />}
        {kind === 'volumesnapshotlocations' && <VeleroVSLRenderer data={data} />}
        {kind === 'externalsecrets' && <ExternalSecretRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'clusterexternalsecrets' && <ClusterExternalSecretRenderer data={data} onNavigate={onNavigate} />}
        {(kind === 'secretstores' || kind === 'clustersecretstores') && <SecretStoreRenderer data={data} />}
        {kind === 'clusters' && !data?.apiVersion?.includes('cluster.x-k8s.io') && <CNPGClusterRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'clusters' && data?.apiVersion?.includes('cluster.x-k8s.io') && <CAPIClusterRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'scheduledbackups' && <CNPGScheduledBackupRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'poolers' && <CNPGPoolerRenderer data={data} onNavigate={onNavigate} />}
        {/* Cluster API (CAPI) */}
        {'topology.cluster.x-k8s.io/owned' in (data?.metadata?.labels ?? {}) && data?.apiVersion?.includes('cluster.x-k8s.io') && (
          <AlertBanner
            variant="warning"
            title="Topology-controlled — this resource is managed by ClusterClass. Manual changes will be reconciled back."
          />
        )}
        {kind === 'machines' && data?.apiVersion?.includes('cluster.x-k8s.io') && <CAPIMachineRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'machinedeployments' && <CAPIMachineDeploymentRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'machinesets' && data?.apiVersion?.includes('cluster.x-k8s.io') && <CAPIMachineSetRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'machinepools' && <CAPIMachinePoolRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'kubeadmcontrolplanes' && <CAPIKubeadmControlPlaneRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'clusterclasses' && <CAPIClusterClassRenderer data={data} />}
        {kind === 'machinehealthchecks' && <CAPIMachineHealthCheckRenderer data={data} />}
        {kind === 'machinedrainrules' && <CAPIMachineDrainRuleRenderer data={data} />}
        {(kind === 'kubeadmconfigs' || kind === 'kubeadmconfigtemplates') && <CAPIKubeadmConfigRenderer data={data} />}
        {/* AWS CAPI Infrastructure Provider */}
        {kind === 'awsmanagedcontrolplanes' && <AWSManagedControlPlaneRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'awsmanagedmachinepools' && <AWSManagedMachinePoolRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'awsmachines' && <AWSMachineRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'awsmachinetemplates' && <AWSMachineTemplateRenderer data={data} />}
        {kind === 'awsmanagedclusters' && <AWSManagedClusterRenderer data={data} />}
        {/* GCP CAPI Infrastructure Provider */}
        {kind === 'gcpmanagedcontrolplanes' && <GCPManagedControlPlaneRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'gcpmanagedmachinepools' && <GCPManagedMachinePoolRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'gcpmachines' && <GCPMachineRenderer data={data} onNavigate={onNavigate} />}
        {/* Azure CAPI Infrastructure Provider */}
        {kind === 'azuremanagedcontrolplanes' && <AzureManagedControlPlaneRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'azuremanagedmachinepools' && <AzureManagedMachinePoolRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'azuremachines' && <AzureMachineRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'virtualservices' && <IstioVirtualServiceRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'destinationrules' && <IstioDestinationRuleRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'serviceentries' && <IstioServiceEntryRenderer data={data} />}
        {kind === 'peerauthentications' && <IstioPeerAuthenticationRenderer data={data} />}
        {kind === 'authorizationpolicies' && <IstioAuthorizationPolicyRenderer data={data} />}
        {kind === 'mutatingwebhookconfigurations' && <WebhookConfigRenderer data={data} isMutating />}
        {kind === 'validatingwebhookconfigurations' && <WebhookConfigRenderer data={data} />}
        {kind === 'ingressclasses' && <IngressClassRenderer data={data} />}
        {kind === 'priorityclasses' && <PriorityClassRenderer data={data} />}
        {kind === 'runtimeclasses' && <RuntimeClassRenderer data={data} />}
        {kind === 'leases' && <LeaseRenderer data={data} />}
        {/* Knative Serving */}
        {(kind === 'services' && data?.apiVersion?.includes('serving.knative.dev')) && <KnativeServiceRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'knativeservices' && <KnativeServiceRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'knativeconfigurations' && <KnativeConfigurationRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'knativerevisions' && <KnativeRevisionRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'knativeroutes' && <KnativeRouteRenderer data={data} onNavigate={onNavigate} />}
        {(kind === 'ingresses' && data?.apiVersion?.includes('networking.internal.knative.dev')) && <KnativeIngressRenderer data={data} />}
        {kind === 'knativeingresses' && <KnativeIngressRenderer data={data} />}
        {(kind === 'certificates' && data?.apiVersion?.includes('networking.internal.knative.dev')) && <KnativeCertificateRenderer data={data} />}
        {kind === 'knativecertificates' && <KnativeCertificateRenderer data={data} />}
        {kind === 'serverlessservices' && <ServerlessServiceRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'domainmappings' && <DomainMappingRenderer data={data} onNavigate={onNavigate} />}
        {/* Knative Eventing */}
        {kind === 'brokers' && <BrokerRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'triggers' && <TriggerRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'pingsources' && <PingSourceRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'apiserversources' && <ApiServerSourceRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'containersources' && <ContainerSourceRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'sinkbindings' && <SinkBindingRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'eventtypes' && <EventTypeRenderer data={data} />}
        {kind === 'channels' && <ChannelRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'inmemorychannels' && <InMemoryChannelRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'subscriptions' && <SubscriptionRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'sequences' && <SequenceRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'parallels' && <ParallelRenderer data={data} onNavigate={onNavigate} />}

        {/* Traefik */}
        {(kind === 'ingressroutes' || kind === 'ingressroutetcps' || kind === 'ingressrouteudps') && <TraefikIngressRouteRenderer data={data} onNavigate={onNavigate} />}
        {(kind === 'middlewares' || kind === 'middlewaretcps') && <TraefikMiddlewareRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'traefikservices' && <TraefikServiceRenderer data={data} onNavigate={onNavigate} />}
        {(kind === 'serverstransports' || kind === 'serverstransporttcps') && <TraefikServersTransportRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'tlsoptions' && <TraefikTLSOptionRenderer data={data} />}

        {/* Contour */}
        {kind === 'httpproxies' && <ContourHTTPProxyRenderer data={data} onNavigate={onNavigate} />}

        {/* Crossplane — kind-dispatched for the static package/config kinds, spec-shape
            detected for MR/XR/Claim (their plurals are unbounded). */}
        {kind === 'providers' && data?.apiVersion?.startsWith('pkg.crossplane.io/') && <CrossplanePackageRenderer data={data} kindLabel="Provider" onNavigate={onNavigate} />}
        {kind === 'functions' && data?.apiVersion?.startsWith('pkg.crossplane.io/') && <CrossplanePackageRenderer data={data} kindLabel="Function" onNavigate={onNavigate} />}
        {kind === 'configurations' && data?.apiVersion?.startsWith('pkg.crossplane.io/') && <CrossplanePackageRenderer data={data} kindLabel="Configuration" onNavigate={onNavigate} />}
        {kind === 'providerconfigs' && <CrossplaneProviderConfigRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'compositeresourcedefinitions' && <XRDRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'compositions' && data?.apiVersion?.startsWith('apiextensions.crossplane.io/') && <CompositionRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'compositionrevisions' && <CompositionRevisionRenderer data={data} onNavigate={onNavigate} />}
        {isCrossplaneMR && <ManagedResourceRenderer data={data} onNavigate={onNavigate} />}
        {(isCrossplaneXR || isCrossplaneClaim) && <CompositeComp data={data} onNavigate={onNavigate} />}

        {/* Generic renderer for CRDs and unknown resource types — also fires
            for known-plural collisions where no apiVersion-gated renderer
            matched (e.g. a Knative Configuration sharing the `configurations`
            plural with Crossplane Configuration). */}
        {(!isKnownKind || crossplaneCollisionFallthrough) && <GenericRenderer data={data} />}

        {/* Common sections - can be disabled when parent handles them separately */}
        {showCommonSections && (
          <>
            <AppInfoSection data={data} />
            <ExternalLinksSection data={data} />

            {/* Prometheus Metrics Charts — skip for Pending pods, and when parent has dedicated Metrics tab */}
            {showMetrics && renderMetrics && !(kind === 'pods' && data?.status?.phase === 'Pending') && (
              renderMetrics({ kind: data?.kind || resource.kind, namespace: resource.namespace, name: resource.name })
            )}

            {/* Sidebar sections rendered inline when no renderSidebar */}
            {!renderSidebar && sidebarContent}
          </>
        )}
        {mainFooter}
      </div>
      {renderSidebar && sidebarContent && renderSidebar(sidebarContent)}
    </div>
  )
}

// ============================================================================
// RESOURCE STATUS HELPER
// ============================================================================

// Coarse health hint for the per-resource Diagnose entry point: should the action
// read as an urgent "Diagnose" (resource has a live problem) or a quiet "ask AI"?
// Derived from the same status the detail badge shows, so the button matches what
// the user sees. We key off the StatusBadge `level` (the workload/pod status fns
// return it) — NOT the color string, which varies by helper. 'unknown' for kinds
// without a level (we don't assert "Diagnose" when we can't tell → quiet variant).
export type DiagnoseHealthHint = 'problem' | 'healthy' | 'unknown'
export function diagnoseHealthHint(kind: string, data: any): DiagnoseHealthHint {
  const st = getResourceStatus(kind, data) as { level?: string } | null
  switch (st?.level) {
    case 'unhealthy':
    case 'degraded':
    case 'alert':
      return 'problem'
    case 'healthy':
      return 'healthy'
    default:
      return 'unknown'
  }
}

export function getResourceStatus(kind: string, data: any): { text: string; color: string } | null {
  if (!data) return null
  const k = kind.toLowerCase()

  if (k === 'pods') return getPodStatus(data)
  if (['deployments', 'statefulsets', 'replicasets', 'daemonsets'].includes(k)) return getWorkloadStatus(data, k)
  if (k === 'services') {
    if (data.apiVersion?.includes('serving.knative.dev')) {
      const status = getKnativeConditionStatus(data)
      return { text: status.text, color: status.color }
    }
    return getServiceStatus(data)
  }
  if (k === 'endpointslices') {
    const endpoints = data.endpoints || []
    const ready = endpoints.filter((endpoint: any) => endpoint?.conditions?.ready !== false).length
    const text = endpoints.length === 0 ? 'No endpoints' : `${ready}/${endpoints.length} ready`
    const color = endpoints.length === 0 ? SEVERITY_BADGE.neutral :
      ready === endpoints.length ? SEVERITY_BADGE.success :
      ready > 0 ? SEVERITY_BADGE.warning :
      SEVERITY_BADGE.error
    return { text, color }
  }
  if (k === 'jobs') return getJobStatus(data)
  if (k === 'cronjobs') return getCronJobStatus(data)
  if (k === 'hpas' || k === 'horizontalpodautoscalers') return getHPAStatus(data)
  if (k === 'nodes') return getNodeStatus(data)
  if (k === 'persistentvolumeclaims') return getPVCStatus(data)
  if (k === 'rollouts') return getRolloutStatus(data)
  if (k === 'workflows') return getWorkflowStatus(data)
  if (k === 'cronworkflows') return getCronWorkflowStatus(data)
  if (k === 'certificates') {
    if (data.apiVersion?.includes('networking.internal.knative.dev')) {
      const status = getKnativeConditionStatus(data)
      return { text: status.text, color: status.color }
    }
    return getCertificateStatus(data)
  }
  if (k === 'persistentvolumes') return getPVStatus(data)
  if (k === 'certificaterequests') return getCertificateRequestStatus(data)
  if (k === 'clusterissuers') return getClusterIssuerStatus(data)
  if (k === 'issuers') return getIssuerStatus(data)
  if (k === 'orders') return getOrderState(data)
  if (k === 'challenges') return getChallengeState(data)
  if (k === 'gateways') {
    if (data.apiVersion?.includes('networking.istio.io')) return getIstioGatewayStatus(data)
    return getGatewayStatus(data)
  }
  if (k === 'gatewayclasses') return getGatewayClassStatus(data)
  if (k === 'httproutes' || k === 'grpcroutes' || k === 'tcproutes' || k === 'tlsroutes') return getRouteStatus(data)
  if (k === 'sealedsecrets') return getSealedSecretStatus(data)
  if (k === 'poddisruptionbudgets') return getPDBStatus(data)
  if (k === 'gitrepositories') return getGitRepositoryStatus(data)
  if (k === 'ocirepositories') return getOCIRepositoryStatus(data)
  if (k === 'helmrepositories') return getHelmRepositoryStatus(data)
  if (k === 'kustomizations') return getKustomizationStatus(data)
  if (k === 'helmreleases') return getFluxHelmReleaseStatus(data)
  if (k === 'alerts') return getFluxAlertStatus(data)
  if (k === 'applications') return getArgoApplicationStatus(data)
  if (k === 'nodepools') return getNodePoolStatus(data)
  if (k === 'nodeclaims') return getNodeClaimStatus(data)
  if (k === 'ec2nodeclasses') return getEC2NodeClassStatus(data)
  if (k === 'scaledobjects') return getScaledObjectStatus(data)
  if (k === 'scaledjobs') return getScaledJobStatus(data)
  if (k === 'servicemonitors') return getServiceMonitorStatus(data)
  if (k === 'prometheusrules') return getPrometheusRuleStatus(data)
  if (k === 'podmonitors') return getPodMonitorStatus(data)
  if (k === 'vulnerabilityreports') return getVulnerabilityReportStatus(data)
  if (k === 'configauditreports') return getConfigAuditReportStatus(data)
  if (k === 'exposedsecretreports') return getExposedSecretReportStatus(data)
  if (k === 'rbacassessmentreports' || k === 'clusterrbacassessmentreports' || k === 'infraassessmentreports' || k === 'clusterinfraassessmentreports') return getRbacAssessmentReportStatus(data)
  if (k === 'clustercompliancereports') return getClusterComplianceReportStatus(data)
  if (k === 'sbomreports' || k === 'clustersbomreports') return getSbomReportStatus(data)
  if (k === 'policyreports' || k === 'clusterpolicyreports') return getPolicyReportStatus(data)
  if (k === 'kyvernopolicies' || k === 'clusterpolicies') {
    if (data?.apiVersion?.startsWith('nvidia.com/')) return getNvidiaClusterPolicyStatus(data)
    return getKyvernoPolicyStatus(data)
  }
  if (k === 'nvidiadrivers') return getNvidiaDriverStatus(data)
  if (k === 'resourceclaims') return getResourceClaimStatus(data)
  if (k === 'resourceclaimtemplates') return getResourceClaimTemplateStatus(data)
  if (k === 'deviceclasses') return getDeviceClassStatus(data)
  if (k === 'resourceslices') return getResourceSliceStatus(data)
  if (k === 'backups') {
    if (data.apiVersion?.includes('cnpg.io')) return getCNPGBackupStatus(data)
    return getBackupStatus(data)
  }
  if (k === 'restores') return getRestoreStatus(data)
  if (k === 'schedules') return getScheduleStatus(data)
  if (k === 'backupstoragelocations') return getBSLStatus(data)
  if (k === 'externalsecrets') return getExternalSecretStatus(data)
  if (k === 'clusterexternalsecrets') return getClusterExternalSecretStatus(data)
  if (k === 'secretstores') return getSecretStoreStatus(data)
  if (k === 'clustersecretstores') return getClusterSecretStoreStatus(data)
  if (k === 'clusters') {
    if (data.apiVersion?.includes('cluster.x-k8s.io')) return getCAPIClusterStatus(data)
    return getCNPGClusterStatus(data)
  }
  if (k === 'machines' && data.apiVersion?.includes('cluster.x-k8s.io')) return getMachineStatus(data)
  if (k === 'machinedeployments') return getMachineDeploymentStatus(data)
  if (k === 'machinesets') return getMachineSetStatus(data)
  if (k === 'machinepools') return getMachinePoolStatus(data)
  if (k === 'kubeadmcontrolplanes') return getKCPStatus(data)
  if (k === 'clusterclasses') return getClusterClassStatus(data)
  if (k === 'machinehealthchecks') return getMachineHealthCheckStatus(data)
  // AWS CAPI Infrastructure Provider
  if (k === 'awsmanagedcontrolplanes') return getAWSMCPStatus(data)
  if (k === 'awsmanagedmachinepools') return getAWSMMPStatus(data)
  if (k === 'awsmachines') return getAWSMachineStatus(data)
  if (k === 'awsmanagedclusters') return getAWSManagedClusterStatus(data)
  // GCP CAPI Infrastructure Provider
  if (k === 'gcpmanagedcontrolplanes') return getGCPMCPStatus(data)
  if (k === 'gcpmanagedmachinepools') return getGCPMMPStatus(data)
  if (k === 'gcpmachines') return getGCPMachineStatus(data)
  if (k === 'gcpmanagedclusters') return getGCPManagedClusterStatus(data)
  // Azure CAPI Infrastructure Provider
  if (k === 'azuremanagedcontrolplanes') return getAzureMCPStatus(data)
  if (k === 'azuremanagedmachinepools') return getAzureMMPStatus(data)
  if (k === 'azuremachines') return getAzureMachineStatus(data)
  if (k === 'azuremanagedclusters') return getAzureManagedClusterStatus(data)
  if (k === 'scheduledbackups') return getCNPGScheduledBackupStatus(data)
  if (k === 'poolers') return getCNPGPoolerStatus(data)
  if (k === 'virtualservices') return getVirtualServiceStatus(data)
  if (k === 'destinationrules') return getDestinationRuleStatus(data)
  if (k === 'serviceentries') return getServiceEntryStatus(data)
  if (k === 'peerauthentications') return getPeerAuthenticationStatus(data)
  if (k === 'authorizationpolicies') return getAuthorizationPolicyStatus(data)

  // Crossplane — Provider/Function/Configuration share package conditions;
  // ProviderConfig has its own; MR/XR/Claim detected by spec shape.
  if ((k === 'providers' || k === 'functions' || k === 'configurations') && data?.apiVersion?.startsWith('pkg.crossplane.io/')) {
    return getProviderStatus(data)
  }
  if (k === 'providerconfigs') return getProviderConfigStatus(data)
  if (isManagedResource(data) || isComposite(data) || isClaim(data)) {
    return getCrossplaneStatus(data)
  }

  // Contour HTTPProxy
  if (k === 'httpproxies') {
    const s = getHTTPProxyStatus(data)
    if (s.status === 'healthy') return { text: s.label, color: SEVERITY_BADGE.success }
    if (s.status === 'unhealthy') return { text: s.label, color: SEVERITY_BADGE.error }
    if (s.status === 'degraded') return { text: s.label, color: SEVERITY_BADGE.warning }
    return null
  }

  // Knative Revisions have custom status logic (scaled-to-zero, activating)
  if (k === 'knativerevisions') {
    const status = getRevisionStatus(data)
    return { text: status.text, color: status.color }
  }

  // All other Knative resources use the standard Ready condition pattern
  const knativeConditionKinds = [
    'knativeservices', 'knativeconfigurations', 'knativeroutes',
    'brokers', 'triggers',
    'pingsources', 'apiserversources', 'containersources', 'sinkbindings',
    'channels', 'inmemorychannels', 'subscriptions',
    'sequences', 'parallels',
    'domainmappings', 'knativeingresses', 'knativecertificates', 'serverlessservices',
  ]
  if (knativeConditionKinds.includes(k) || (k === 'ingresses' && data.apiVersion?.includes('networking.internal.knative.dev'))) {
    const status = getKnativeConditionStatus(data)
    return { text: status.text, color: status.color }
  }

  // Generic status extraction
  const status = data.status
  if (status) {
    if (status.phase) {
      const phase = String(status.phase)
      const healthyPhases = ['Running', 'Active', 'Succeeded', 'Ready', 'Healthy', 'Available', 'Bound']
      const warningPhases = ['Pending', 'Progressing', 'Unknown', 'Terminating']
      const isHealthy = healthyPhases.includes(phase)
      const isWarning = warningPhases.includes(phase)
      return {
        text: phase,
        color: isHealthy ? SEVERITY_BADGE.success :
               isWarning ? SEVERITY_BADGE.warning :
               SEVERITY_BADGE.error
      }
    }

    if (status.conditions && Array.isArray(status.conditions)) {
      const readyCondition = status.conditions.find((c: any) =>
        c.type === 'Ready' || c.type === 'Available' || c.type === 'Progressing'
      )
      if (readyCondition) {
        const isReady = readyCondition.status === 'True'
        return {
          text: isReady ? 'Ready' : 'Not Ready',
          color: isReady ? SEVERITY_BADGE.success : SEVERITY_BADGE.warning
        }
      }
    }
  }

  return null
}
