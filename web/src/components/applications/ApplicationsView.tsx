import { useCallback, useEffect, useMemo } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import {
  ApplicationsList,
  ApplicationDetail,
  CenteredEmpty,
  PageHeader,
  FreshnessControl,
  useToast,
  orderEnvs,
  matchWorkloadAcrossInstances,
  workloadKey,
  healthOf,
  compareVersions,
  gitOpsRouteForKind,
  type AppRow,
  type AppIdentityInstance,
  type ApplicationView,
  type AppSourceRef,
  type SelectedAppWorkload,
  type SelectedResource,
} from '@skyhook-io/k8s-ui'
import { Boxes } from 'lucide-react'
import { useApplicationHistory, useApplications, useTopology } from '../../api/client'
import { useConnection } from '../../context/ConnectionContext'
import { buildWorkloadPath, kindToPlural } from '../../utils/navigation'
import { WorkloadView } from '../workload/WorkloadView'

const APPLICATION_VIEWS: ReadonlySet<ApplicationView> = new Set<ApplicationView>(['overview', 'topology', 'history'])

function parseApplicationView(value: string | null): ApplicationView {
  if (!value || !APPLICATION_VIEWS.has(value as ApplicationView)) return 'overview'
  return value as ApplicationView
}

interface ApplicationsViewProps {
  namespaces: string[]
  onOpenResource: (resource: SelectedResource) => void
}

export function ApplicationsView({ namespaces, onOpenResource }: ApplicationsViewProps) {
  const query = useApplications(namespaces)
  const { connection } = useConnection()
  const apps = useMemo(() => query.data?.applications ?? [], [query.data])

  const freshness = (
    <FreshnessControl
      mode="auto"
      dataUpdatedAt={query.dataUpdatedAt}
      onRefresh={() => query.refetch()}
      connectionState={connection.state}
    />
  )

  // Which app is open lives in the URL (?app=<key>) so the detail view is
  // deep-linkable and the browser back button returns to the list. Opening or
  // closing an app also clears the per-app params (view, workload, tab).
  const [searchParams, setSearchParams] = useSearchParams()
  const selectedKey = searchParams.get('app')
  const selected = useMemo(() => apps.find((a) => a.key === selectedKey) ?? null, [apps, selectedKey])

  const selectApp = useCallback(
    (key: string | null) => {
      const params = new URLSearchParams(searchParams)
      if (key) params.set('app', key)
      else params.delete('app')
      params.delete('view')
      params.delete('workload')
      params.delete('tab')
      setSearchParams(params)
    },
    [searchParams, setSearchParams],
  )

  // A stale ?app= (uninstalled/renamed app, or a link from another cluster)
  // would leave the URL lying under the list view — clear it once data is
  // fresh. Never during load, so a slow fetch can't eject a valid deep link.
  useEffect(() => {
    if (selectedKey && !selected && query.isSuccess) {
      const params = new URLSearchParams(searchParams)
      params.delete('app')
      params.delete('view')
      params.delete('workload')
      params.delete('tab')
      setSearchParams(params, { replace: true })
    }
  }, [selectedKey, selected, query.isSuccess, searchParams, setSearchParams])

  if (selectedKey && selected) {
    return <AppDetailRoute app={selected} apps={apps} onBack={() => selectApp(null)} onOpenResource={onOpenResource} />
  }

  // The header + status + filters + table chassis lives inside ApplicationsList
  // (mirroring GitOpsTableView), which renders only on the data path. To keep
  // the page header from vanishing while loading / on error, the wrapper shows
  // the same header bar above those states. (Keep title + description in sync
  // with ApplicationsList's PageHeader.)
  if (query.isLoading) {
    return (
      <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
        <ApplicationsList apps={[]} onSelect={selectApp} headerActions={freshness} loading />
      </div>
    )
  }
  if (query.error) {
    return (
      <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
        <div className="shrink-0 border-b border-theme-border px-4 py-4">
          <PageHeader
            icon={Boxes}
            title="Applications"
            description="Deployable software in this cluster — your services, workers, and jobs, grouped by app/release evidence."
          />
        </div>
        <CenteredEmpty tone="filtered" icon={Boxes} headline="Failed to load applications" body={(query.error as Error).message} />
      </div>
    )
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <ApplicationsList apps={apps} onSelect={selectApp} headerActions={freshness} />
    </div>
  )
}

// AppDetailRoute wires the OSS data hooks the shared ApplicationDetail can't:
// the resources-view topology over the app's namespaces and the per-workload
// WorkloadView. Split out so useTopology runs unconditionally (Rules of Hooks).
function AppDetailRoute({ app, apps, onBack, onOpenResource }: { app: AppRow; apps: AppRow[]; onBack: () => void; onOpenResource: (resource: SelectedResource) => void }) {
  const navigate = useNavigate()
  const appNamespaces = useMemo(
    () => Array.from(new Set((app.workloads ?? []).map((w) => w.namespace).filter(Boolean))).sort(),
    [app.workloads],
  )
  const appHistoryNamespaces = useMemo(() => {
    const namespaces = new Set(appNamespaces)
    if (app.sourceRef?.namespace) namespaces.add(app.sourceRef.namespace)
    return Array.from(namespaces).sort()
  }, [app.sourceRef?.namespace, appNamespaces])
  const { data: topology, isLoading: topologyLoading } = useTopology(appNamespaces, 'resources', { enabled: appNamespaces.length > 0 })

  // The selected workload (?workload=<key>) is the scope switch and wins over
  // ?view= when both are present. With neither param, use the product default:
  // multi-workload apps open on app overview, single-workload apps open on the
  // workload. A single-workload app does not expose app scope.
  const [searchParams, setSearchParams] = useSearchParams()
  const viewParam = searchParams.get('view')
  const selectedView = parseApplicationView(viewParam)
  const selectedWorkloadParam = searchParams.get('workload')
  const appWorkloads = app.workloads ?? []
  const singleWorkloadKey = appWorkloads.length === 1 ? workloadKey(appWorkloads[0]) : null
  const selectedWorkloadKey = singleWorkloadKey ?? selectedWorkloadParam
  const historyQuery = useApplicationHistory(app.key, appHistoryNamespaces, { enabled: !selectedWorkloadKey })
  useEffect(() => {
    if (!singleWorkloadKey) return
    if (selectedWorkloadParam === singleWorkloadKey && !viewParam) return
    const params = new URLSearchParams(searchParams)
    params.delete('view')
    params.set('workload', singleWorkloadKey)
    setSearchParams(params, { replace: true })
  }, [searchParams, selectedWorkloadParam, setSearchParams, singleWorkloadKey, viewParam])
  const selectView = useCallback(
    (view: ApplicationView) => {
      const params = new URLSearchParams(searchParams)
      params.delete('tab')
      if (singleWorkloadKey) {
        params.delete('view')
        params.set('workload', singleWorkloadKey)
      } else if (view === 'overview') {
        params.delete('view')
        params.delete('workload')
      } else {
        params.set('view', view)
        params.delete('workload')
      }
      setSearchParams(params)
    },
    [searchParams, setSearchParams, singleWorkloadKey],
  )
  const selectWorkload = useCallback(
    (key: string | null) => {
      const params = new URLSearchParams(searchParams)
      if (key) {
        const wasInWorkloadScope = !!selectedWorkloadKey
        params.delete('view')
        params.set('workload', key)
        if (!wasInWorkloadScope) params.delete('tab')
      } else if (singleWorkloadKey) {
        params.delete('view')
        params.set('workload', singleWorkloadKey)
      } else {
        params.delete('workload')
        params.delete('tab')
        params.delete('view')
      }
      setSearchParams(params)
    },
    [searchParams, selectedWorkloadKey, setSearchParams, singleWorkloadKey],
  )
  const openWorkloadResource = useCallback(
    (resource: SelectedResource) => {
      if (kindToPlural(resource.kind).toLowerCase() !== 'pods') {
        onOpenResource(resource)
        return
      }

      const [pathname, rawSearch = ''] = buildWorkloadPath({ ...resource, kind: kindToPlural(resource.kind) }).split('?')
      const params = new URLSearchParams(rawSearch)
      const activeNamespaces = searchParams.get('namespaces')
      if (activeNamespaces) params.set('namespaces', activeNamespaces)
      navigate({ pathname, search: params.toString() })
    },
    [navigate, onOpenResource, searchParams],
  )
  const openSource = useCallback(
    (source: AppSourceRef) => {
      if (source.type === 'gitops') {
        const path = gitOpsRouteForKind(source.kind, source.namespace, source.name)
        if (path) navigate(path)
        return
      }
      if (source.type === 'helm') {
        const params = new URLSearchParams()
        const activeNamespaces = searchParams.get('namespaces')
        if (activeNamespaces) params.set('namespaces', activeNamespaces)
        params.set('release', `${source.namespace}/${source.name}`)
        navigate({ pathname: '/helm', search: params.toString() })
      }
    },
    [navigate, searchParams],
  )

  // App identity switcher data: this instance's siblings (ladder-ordered
  // digests). It switches between REAL instances — ?app= changes, deep links
  // stay instance-keyed.
  const { showToast } = useToast();
  const identityInstances = useMemo<AppIdentityInstance[] | null>(() => {
    const fam = app.identity;
    if (!fam) return null;
    const sibs = apps.filter((a) => a.identity?.key === fam.key);
    if (sibs.length < 2) return null;
    const newest = (a: AppRow) =>
      (a.versions ?? []).reduce<string | undefined>((best, v) => (!best || compareVersions(v, best) === 1 ? v : best), undefined) ?? a.appVersion;
    const order = orderEnvs(sibs.map((a) => a.identity!.env));
    return [...sibs]
      .sort((a, b) => order.indexOf(a.identity!.env) - order.indexOf(b.identity!.env) || a.name.localeCompare(b.name))
      .map((a) => ({
        appKey: a.key,
        name: a.name,
        env: a.identity!.env,
        health: healthOf(a.health),
        version: newest(a),
        confidence: a.identity!.confidence,
        evidence: a.identity!.evidence,
      }));
  }, [apps, app]);

  // Position-preserving env switch: carry the selected workload + tab into the
  // sibling when a matching workload exists there (exact kind+name, else the
  // env-affix-stripped stem); otherwise land on the instance overview and say
  // the workload wasn't found.
  const switchInstance = useCallback(
    (targetKey: string) => {
      const target = apps.find((a) => a.key === targetKey);
      const params = new URLSearchParams(searchParams);
      params.set('app', targetKey);
      const wk = params.get('workload');
      let matched = false;
      if (wk && target) {
        // Stem matching strips this app group's own env tokens too, so
        // discovered envs (loadtest, …) carry position like the trio does.
        const identityEnvs = new Set((identityInstances ?? []).map((i) => i.env));
        const m = matchWorkloadAcrossInstances(wk, target.workloads, identityEnvs);
        if (m) {
          params.set('workload', workloadKey(m));
          matched = true;
        }
      }
      if (!matched && wk) {
        // A workload WAS selected but has no counterpart — land on the target
        // instance's default scope and say so. Single-workload instances default
        // to their workload; composed apps default to app overview.
        params.delete('workload');
        params.delete('tab');
        const soleTargetWorkload = target?.workloads?.length === 1 ? target.workloads[0] : null;
        if (soleTargetWorkload) {
          params.delete('view');
          params.set('workload', workloadKey(soleTargetWorkload));
        } else {
          params.delete('view');
        }
        if (target) {
          showToast(`No matching workload in ${target.identity?.env ?? target.name}`, { detail: soleTargetWorkload ? 'Showing the instance workload instead.' : 'Showing the instance overview instead.', type: 'info' });
        }
      }
      setSearchParams(params);
    },
    [apps, identityInstances, searchParams, setSearchParams, showToast],
  );

  const discoveredEnvs = useMemo(
    () => new Set(apps.map((a) => a.identity?.env).filter((e): e is string => !!e)),
    [apps],
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <ApplicationDetail
        app={app}
        onBack={onBack}
        topology={topology}
        topologyLoading={topologyLoading}
        identityInstances={identityInstances}
        onSwitchInstance={switchInstance}
        discoveredEnvs={discoveredEnvs}
        onNavigateToResource={onOpenResource}
        history={historyQuery.data}
        historyLoading={historyQuery.isLoading}
        onOpenSource={openSource}
        selectedWorkloadKey={selectedWorkloadKey}
        onSelectWorkload={selectWorkload}
        selectedView={selectedView}
        onSelectView={selectView}
        renderWorkload={(workload: SelectedAppWorkload) => (
          <div className="h-full overflow-hidden">
            <WorkloadView
              kind={kindToPlural(workload.kind)}
              namespace={workload.namespace}
              name={workload.name}
              onBack={() => selectWorkload(null)}
              hideBackButton
              compactHeader
              pushTabHistory
              onNavigateToResource={openWorkloadResource}
            />
          </div>
        )}
      />
    </div>
  )
}
