import { Link } from 'react-router-dom'
import { TimeValue } from '@skyhook-io/k8s-ui'
import { JAVA4SHELL_CVE, useJava4ShellCheck, type Java4ShellFinding, type Java4ShellResult } from '../../api/vulnerabilities'
import type { SelectedResource } from '../../types'
import { resourceCoverageNotice, resourceNarrowedNotice } from '@skyhook-io/k8s-ui/utils/resource-coverage'
import { pluralize } from '@skyhook-io/k8s-ui/utils/pluralize'
import { resourceRefKey } from '@skyhook-io/k8s-ui/utils/resource-refs'
import { ImageName } from '../vulnerabilities/parts'

const advisoryURL = 'https://www.oracle.com/security-alerts/cspunov2026.html#AppendixJAVA'

interface Java4ShellPanelProps {
  namespaces: string[]
  context: string
  connected: boolean
  trivySearch?: string
  onNavigateToResource: (resource: SelectedResource) => void
}

export function Java4ShellPanel({ namespaces, context, connected, trivySearch, onNavigateToResource }: Java4ShellPanelProps) {
  const { data, dataUpdatedAt, isFetching, error, refetch } = useJava4ShellCheck(namespaces, context)
  return (
    <section aria-label="Java4Shell check" className="rounded-xl border border-theme-border bg-theme-surface p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <h2 className="text-sm font-semibold text-theme-text-primary">Java4Shell</h2>
          <p className="text-xs text-theme-text-secondary">
            Check running workloads for affected Java versions and prioritize patching with current node evidence.
          </p>
          <p className="text-xs text-theme-text-tertiary">Radar reads version declarations from image registries that allow anonymous access.</p>
          <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
            <a className="text-accent-text hover:underline" href={advisoryURL} target="_blank" rel="noreferrer">{JAVA4SHELL_CVE} advisory</a>
            {trivySearch && <Link className="text-accent-text hover:underline" to={{ search: trivySearch }}>Find in Trivy reports</Link>}
          </div>
        </div>
        <button
          type="button"
          className="btn-brand shrink-0 px-3 py-1.5 text-xs font-medium"
          onClick={() => void refetch()}
          disabled={!connected || isFetching}
        >
          {isFetching ? 'Checking workloads…' : data ? 'Run check again' : 'Check for Java4Shell'}
        </button>
      </div>
      {isFetching && <p role="status" className="mt-3 text-xs text-theme-text-secondary">Reading running Pods and image metadata. This can take up to 40 seconds after the Pods are read.</p>}
      {error && <p role="alert" className="mt-3 text-xs text-warning-text">{error.message}{data && ' Showing the previous result.'}</p>}
      {data && <Java4ShellResults data={data} checkedAt={new Date(dataUpdatedAt).toISOString()} onOpen={onNavigateToResource} />}
    </section>
  )
}

const reasonLabels: Record<string, string> = {
  image_identity_unknown: 'missing a running image digest',
  version_unknown: 'with unsupported Java versions',
  platform_unknown: 'with unknown image platforms',
  auth_required: 'required registry sign-in',
  not_found: 'with missing or private images',
  blocked: 'with registry addresses Radar could not reach',
  rate_limited: 'limited by the registry',
  error: 'with failed registry reads',
  not_checked: 'beyond the check limit',
}

function Java4ShellResults({ data, checkedAt, onOpen }: { data: Java4ShellResult; checkedAt: string; onOpen: (resource: SelectedResource) => void }) {
  const missingDeclaration = data.unassessed.no_java_declaration ?? 0
  const reasons = Object.entries(data.unassessed).filter(([reason]) => reason !== 'no_java_declaration').sort((a, b) => b[1] - a[1])
  const unassessed = reasons.reduce((count, [, n]) => count + n, 0)
  const coverage = resourceCoverageNotice(data.coverage, 'Pods') ?? resourceNarrowedNotice(data.coverage)
  const namespaces = data.coverage.read?.namespaces
  const scope = namespaces?.names && namespaces.names.length <= 3
    ? namespaces.names.join(', ')
    : namespaces ? pluralize(namespaces.count, 'namespace') : 'all namespaces'
  const groups = new Map<string, Java4ShellFinding[]>()
  for (const row of data.findings) {
    const key = resourceRefKey(row.workload)
    const rows = groups.get(key)
    if (rows) rows.push(row)
    else groups.set(key, [row])
  }
  const summary = data.matches > 0
    ? `${pluralize(data.matches, 'container')} with a declared Java version in an affected range`
    : data.containers === 0
      ? 'Radar found no running containers in the scope it read'
      : missingDeclaration === data.containers
        ? 'Images checked declared no Java version'
        : unassessed + missingDeclaration === data.containers
          ? 'Radar could not assess the Java version of any running container'
          : 'No matches among containers whose declared Java version Radar could assess'
  return (
    <div className="mt-3 flex flex-col gap-2 border-t border-theme-border pt-3 text-xs">
      <div className="flex flex-wrap justify-between gap-x-3 gap-y-1">
        <p role="status" className="font-medium text-theme-text-primary">{summary}</p>
        <p className="text-theme-text-tertiary">Checked <TimeValue timestamp={checkedAt} /> · {scope}</p>
      </div>
      {coverage && <p className="text-warning-text">{coverage}</p>}
      {unassessed > 0 && (
        <p className="text-theme-text-secondary">
          Java version unassessed in {pluralize(unassessed, 'container')} ({reasons.map(([reason, count]) => `${count} ${reasonLabels[reason] ?? reason}`).join(', ')}).
        </p>
      )}
      {missingDeclaration > 0 && (
        <p className="text-theme-text-secondary">
          Images for {pluralize(missingDeclaration, 'container')} had no Java version declaration.
        </p>
      )}
      {data.findings.length > 0 && (
        <>
          <p className="text-theme-text-secondary">Image metadata describes the declared Java version. Confirm the deployed JVM and the fix from its vendor.</p>
          <div role="region" aria-label="Matching containers by workload" tabIndex={0} className="max-h-96 overflow-auto rounded-lg border border-theme-border">
            <table className="w-full min-w-[40rem] text-left">
              <thead className="sticky top-0 bg-theme-elevated text-theme-text-secondary">
                <tr>
                  <th scope="col" className="px-3 py-2 font-medium">Workload</th>
                  <th scope="col" className="px-3 py-2 font-medium">Container</th>
                  <th scope="col" className="px-3 py-2 font-medium">Declared Java</th>
                  <th scope="col" className="px-3 py-2 font-medium">Node evidence at check time</th>
                </tr>
              </thead>
              {[...groups.entries()].map(([key, rows]) => (
                <tbody key={key} className="divide-y divide-theme-border border-t border-theme-border">
                  {rows.map((row, index) => (
                    <tr key={JSON.stringify([row.container, row.digest, row.version])} className="align-top">
                      {index === 0 && (
                        <th rowSpan={rows.length} scope="rowgroup" className="px-3 py-2 font-medium">
                          <button type="button" onClick={() => onOpen(row.workload)} className="text-left text-accent-text hover:underline">{row.workload.name}</button>
                          <div className="text-theme-text-tertiary">{row.workload.kind} · {row.workload.namespace}</div>
                        </th>
                      )}
                      <td className="px-3 py-2">
                        <div className="text-theme-text-primary">{row.container}</div>
                        <div className="max-w-48 truncate text-theme-text-tertiary"><ImageName image={row.image} /></div>
                        <div className="text-theme-text-tertiary">{pluralize(row.pods, 'Pod')}</div>
                      </td>
                      <td className="px-3 py-2">
                        <div className="font-mono text-theme-text-primary">{row.version}</div>
                        <div className="text-theme-text-secondary">{row.fixedIn ? `Upstream fix ${row.fixedIn}` : 'Check the vendor for an upgrade'}</div>
                      </td>
                      <td className="px-3 py-2">
                        <JavaPlacement row={row} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              ))}
            </table>
          </div>
        </>
      )}
      {data.findingGroups > data.findings.length && (
        <p className="text-theme-text-secondary">Showing {data.findings.length} of {data.findingGroups} matching workload and image groups. Narrow the namespace scope to see more.</p>
      )}
    </div>
  )
}

function JavaPlacement({ row }: { row: Java4ShellFinding }) {
  const other = row.pods - row.vbmiPods - row.unknownCPUPods
  return (
    <>
      {row.vbmiPods > 0 && <div className="font-medium text-warning-text">Prioritize patching · {pluralize(row.vbmiPods, 'Pod')} on nodes reporting AVX-512 VBMI</div>}
      {row.unknownCPUPods > 0 && <div className="text-theme-text-secondary">CPU capability unknown for {pluralize(row.unknownCPUPods, 'Pod')}</div>}
      {other > 0 && <div className="text-theme-text-secondary">{pluralize(other, 'Pod')} on non-x86 nodes at check time</div>}
      {row.mitigationDeclaredPods > 0 && <div className="text-theme-text-tertiary">Mitigation flag declared in {pluralize(row.mitigationDeclaredPods, 'Pod spec')}</div>}
    </>
  )
}
