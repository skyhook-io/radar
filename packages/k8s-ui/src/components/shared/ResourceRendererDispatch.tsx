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
  PodDisruptionBudgetRenderer,
  ServiceAccountRenderer,
  RoleRenderer,
  RoleBindingRenderer,
  WebhookConfigRenderer,
  EventRenderer,
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
  ContourHTTPProxyRenderer,
} from '../resources/renderers'
import type { SelectedResource, Relationships, ResourceRef, SecretCertificateInfo, ResolvedEnvFrom } from '../../types'
import type { CopyHandler } from '../ui/drawer-components'

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
  }>
  WorkloadRenderer?: React.ComponentType<{
    kind: string; data: any
    onNavigate?: (ref: ResourceRef) => void
  }>
}

// Known resource types with specific renderers (module-level to avoid re-allocation)
const KNOWN_KINDS = new Set([
  'pods', 'deployments', 'statefulsets', 'daemonsets', 'replicasets',
  'services', 'ingresses', 'configmaps', 'secrets', 'jobs', 'cronjobs',
  'hpas', 'horizontalpodautoscalers', 'nodes', 'persistentvolumeclaims',
  'rollouts', 'certificates', 'workflows', 'persistentvolumes',
  'storageclasses', 'certificaterequests', 'clusterissuers', 'issuers',
  'orders', 'challenges',
  'gateways', 'gatewayclasses', 'httproutes', 'grpcroutes', 'tcproutes', 'tlsroutes', 'sealedsecrets', 'workflowtemplates',
  'networkpolicies', 'poddisruptionbudgets', 'serviceaccounts',
  'roles', 'clusterroles', 'rolebindings', 'clusterrolebindings',
  'events', 'gitrepositories', 'ocirepositories', 'helmrepositories',
  'kustomizations', 'helmreleases', 'alerts', 'applications',
  'nodepools', 'nodeclaims', 'ec2nodeclasses', 'scaledobjects', 'scaledjobs',
  'triggerauthentications', 'clustertriggerauthentications',
  'servicemonitors', 'prometheusrules', 'podmonitors',
  'policyreports', 'clusterpolicyreports', 'kyvernopolicies', 'clusterpolicies',
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
  'httpproxies',
])

// ============================================================================
// RESOURCE CONTENT - Delegates to specific renderers
// ============================================================================

interface ResourceRendererDispatchProps {
  resource: SelectedResource
  data: any
  relationships?: Relationships
  certificateInfo?: SecretCertificateInfo
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
  /** Resource events — injected by the platform wrapper */
  events?: any[]
  /** Whether events are still loading */
  eventsLoading?: boolean
  /** Render prop for Prometheus metrics charts — injected by the platform wrapper */
  renderMetrics?: (props: { kind: string; namespace: string; name: string }) => React.ReactNode
}

export function ResourceRendererDispatch({
  resource,
  data,
  relationships,
  certificateInfo,
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
  events,
  eventsLoading,
  renderMetrics,
  resolvedEnvFrom,
  rendererOverrides,
}: ResourceRendererDispatchProps) {
  const kind = resource.kind.toLowerCase()

  const isKnownKind = KNOWN_KINDS.has(kind)

  const PodComp = rendererOverrides?.PodRenderer ?? PodRenderer
  const WorkloadComp = rendererOverrides?.WorkloadRenderer ?? WorkloadRenderer
  const NodeComp = rendererOverrides?.NodeRenderer ?? NodeRenderer
  const ServiceComp = rendererOverrides?.ServiceRenderer ?? ServiceRenderer

  const sidebarContent = showCommonSections && (
    <>
      <RelatedResourcesSection relationships={relationships} onNavigate={onNavigate} />
      {kind !== 'events' && <EventsSection events={events || []} isLoading={eventsLoading ?? false} hint={eventsHint} />}
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
        {['deployments', 'statefulsets', 'daemonsets'].includes(kind) && <WorkloadComp kind={kind} data={data} onNavigate={onNavigate} />}
        {kind === 'replicasets' && <ReplicaSetRenderer data={data} />}
        {kind === 'services' && !data?.apiVersion?.includes('serving.knative.dev') && <ServiceComp data={data} onCopy={onCopy} copied={copied} />}
        {kind === 'ingresses' && !data?.apiVersion?.includes('networking.internal.knative.dev') && <IngressRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'configmaps' && <ConfigMapRenderer data={data} />}
        {kind === 'secrets' && <SecretRenderer data={data} certificateInfo={certificateInfo} resourceData={data} onSaveSecretValue={onSaveSecretValue} isSaving={isSavingSecret} />}
        {kind === 'jobs' && <JobRenderer data={data} />}
        {kind === 'cronjobs' && <CronJobRenderer data={data} onNavigate={onNavigate} />}
        {(kind === 'hpas' || kind === 'horizontalpodautoscalers') && <HPARenderer data={data} onNavigate={onNavigate} />}
        {kind === 'nodes' && <NodeComp data={data} relationships={relationships} />}
        {kind === 'persistentvolumeclaims' && <PVCRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'rollouts' && <RolloutRenderer data={data} />}
        {kind === 'certificates' && !data?.apiVersion?.includes('networking.internal.knative.dev') && <CertificateRenderer data={data} />}
        {kind === 'workflows' && <WorkflowRenderer data={data} />}
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
        {kind === 'sealedsecrets' && <SealedSecretRenderer data={data} />}
        {kind === 'workflowtemplates' && <WorkflowTemplateRenderer data={data} />}
        {kind === 'networkpolicies' && <NetworkPolicyRenderer data={data} />}
        {kind === 'poddisruptionbudgets' && <PodDisruptionBudgetRenderer data={data} />}
        {kind === 'serviceaccounts' && <ServiceAccountRenderer data={data} />}
        {(kind === 'roles' || kind === 'clusterroles') && <RoleRenderer data={data} />}
        {(kind === 'rolebindings' || kind === 'clusterrolebindings') && <RoleBindingRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'events' && <EventRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'gitrepositories' && <GitRepositoryRenderer data={data} />}
        {kind === 'ocirepositories' && <OCIRepositoryRenderer data={data} />}
        {kind === 'helmrepositories' && <HelmRepositoryRenderer data={data} />}
        {kind === 'kustomizations' && <KustomizationRenderer data={data} />}
        {kind === 'helmreleases' && <FluxHelmReleaseRenderer data={data} />}
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
        {(kind === 'kyvernopolicies' || kind === 'clusterpolicies') && <KyvernoPolicyRenderer data={data} />}
        {kind === 'backups' && data.apiVersion?.includes('cnpg.io') && <CNPGBackupRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'backups' && !data.apiVersion?.includes('cnpg.io') && <VeleroBackupRenderer data={data} />}
        {kind === 'restores' && <VeleroRestoreRenderer data={data} />}
        {kind === 'schedules' && <VeleroScheduleRenderer data={data} />}
        {kind === 'backupstoragelocations' && <VeleroBSLRenderer data={data} />}
        {kind === 'volumesnapshotlocations' && <VeleroVSLRenderer data={data} />}
        {kind === 'externalsecrets' && <ExternalSecretRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'clusterexternalsecrets' && <ClusterExternalSecretRenderer data={data} />}
        {(kind === 'secretstores' || kind === 'clustersecretstores') && <SecretStoreRenderer data={data} />}
        {kind === 'clusters' && <CNPGClusterRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'scheduledbackups' && <CNPGScheduledBackupRenderer data={data} onNavigate={onNavigate} />}
        {kind === 'poolers' && <CNPGPoolerRenderer data={data} onNavigate={onNavigate} />}
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

        {/* Contour */}
        {kind === 'httpproxies' && <ContourHTTPProxyRenderer data={data} onNavigate={onNavigate} />}

        {/* Generic renderer for CRDs and unknown resource types */}
        {!isKnownKind && <GenericRenderer data={data} />}

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
      </div>
      {renderSidebar && sidebarContent && renderSidebar(sidebarContent)}
    </div>
  )
}

// ============================================================================
// RESOURCE STATUS HELPER
// ============================================================================

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
  if (k === 'jobs') return getJobStatus(data)
  if (k === 'cronjobs') return getCronJobStatus(data)
  if (k === 'hpas' || k === 'horizontalpodautoscalers') return getHPAStatus(data)
  if (k === 'nodes') return getNodeStatus(data)
  if (k === 'persistentvolumeclaims') return getPVCStatus(data)
  if (k === 'rollouts') return getRolloutStatus(data)
  if (k === 'workflows') return getWorkflowStatus(data)
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
  if (k === 'kyvernopolicies' || k === 'clusterpolicies') return getKyvernoPolicyStatus(data)
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
  if (k === 'clusters') return getCNPGClusterStatus(data)
  if (k === 'scheduledbackups') return getCNPGScheduledBackupStatus(data)
  if (k === 'poolers') return getCNPGPoolerStatus(data)
  if (k === 'virtualservices') return getVirtualServiceStatus(data)
  if (k === 'destinationrules') return getDestinationRuleStatus(data)
  if (k === 'serviceentries') return getServiceEntryStatus(data)
  if (k === 'peerauthentications') return getPeerAuthenticationStatus(data)
  if (k === 'authorizationpolicies') return getAuthorizationPolicyStatus(data)

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
