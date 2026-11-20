import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Download } from 'lucide-react'
import { Collapse, CollapseChevron, TimeValue, useDisclosure } from '@skyhook-io/k8s-ui'
import { JAVA4SHELL_CVE, useJava4ShellCheck, type Java4ShellFinding, type Java4ShellResult, type VulnerabilityWorkloadRef } from '../../api/vulnerabilities'
import type { SelectedResource } from '../../types'
import { triggerDownload } from '@skyhook-io/k8s-ui/utils/download'
import { resourceCoverageNotice, resourceNarrowedNotice } from '@skyhook-io/k8s-ui/utils/resource-coverage'
import { pluralize } from '@skyhook-io/k8s-ui/utils/pluralize'
import { resourceRefKey } from '@skyhook-io/k8s-ui/utils/resource-refs'
import { ImageName } from '../vulnerabilities/parts'
import { Tooltip } from '../ui/Tooltip'

const advisoryURL = 'https://github.com/advisories/GHSA-6g7j-4xh3-xq2q'
const registryAccessURL = 'https://github.com/skyhook-io/radar/blob/main/docs/authentication.md#image-inspection'

interface Java4ShellPanelProps {
  namespaces?: string[]
  context: string
  connected: boolean
  trivySearch?: string
  workload?: VulnerabilityWorkloadRef
  onNavigateToResource?: (resource: SelectedResource) => void
}

export function Java4ShellPanel({ namespaces = [], context, connected, trivySearch, workload, onNavigateToResource }: Java4ShellPanelProps) {
  const { data, dataUpdatedAt, isFetching, error, refetch, progress, cancel } = useJava4ShellCheck(namespaces, context, workload)
  const checkedAt = new Date(dataUpdatedAt).toISOString()
  return (
    <section aria-label="Java4Shell check" className={`rounded-xl border border-theme-border bg-theme-surface ${workload ? 'p-3' : 'p-4'}`}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <h2 className="text-sm font-semibold text-theme-text-primary">Java4Shell</h2>
          <p className="text-xs text-theme-text-secondary">
            {workload
              ? 'Check images in this workload for affected Java version declarations.'
              : 'Check running container images for affected Java version declarations.'}
            {' '}Radar reads registry metadata with the credentials available to this check.
          </p>
          <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
            <a className="text-accent-text hover:underline" href={advisoryURL} target="_blank" rel="noreferrer">{JAVA4SHELL_CVE} advisory</a>
            {trivySearch && <Link className="text-accent-text hover:underline" to={{ search: trivySearch }}>Find in Trivy reports</Link>}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {data && (
            <button
              type="button"
              className="btn-secondary inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium"
              onClick={() => triggerDownload(
                JSON.stringify({ cve: JAVA4SHELL_CVE, checkedAt, ...data }, null, 2),
                'application/json',
                `java4shell-${checkedAt.replace(/[:.]/g, '-')}.json`,
              )}
            >
              <Download className="h-3.5 w-3.5" /> Download JSON
            </button>
          )}
          {cancel && (
            <button type="button" className="btn-secondary shrink-0 px-3 py-1.5 text-xs font-medium" onClick={cancel}>
              Cancel check
            </button>
          )}
          <button
            type="button"
            className="btn-brand shrink-0 px-3 py-1.5 text-xs font-medium"
            onClick={() => void refetch()}
            disabled={!connected || isFetching || Boolean(cancel)}
          >
            {isFetching ? (workload ? 'Checking workload…' : 'Checking workloads…') : cancel ? 'Checking another scope…' : data ? 'Run check again' : 'Check for Java4Shell'}
          </button>
        </div>
      </div>
      {isFetching && (
        <div className="mt-3 space-y-1 text-xs text-theme-text-secondary">
          <p role="status">
            {progress && progress.total > 0
              ? `Checking image groups · ${progress.done} of ${progress.total} processed`
              : 'Reading running Pods and preparing registry lookups.'}
            {data && <span className="sr-only"> Showing the previous result while the check runs.</span>}
          </p>
          <progress
            aria-label="Java4Shell scan progress"
            className="block w-full accent-accent"
            value={progress && progress.total > 0 ? progress.done : undefined}
            max={progress?.total || undefined}
          />
        </div>
      )}
      {error && <p role="alert" className="mt-3 text-xs text-warning-text">{error.message}{data && ' Showing the previous result.'}</p>}
      {data && <Java4ShellResults data={data} checkedAt={checkedAt} onOpen={onNavigateToResource} checkingAgain={isFetching} />}
    </section>
  )
}

const reasonLabels: Record<string, string> = {
  image_identity_unknown: 'Running image digest unavailable',
  version_unknown: 'Unrecognized Java version',
  platform_unknown: 'Image platform unknown',
  auth_required: 'Registry authentication required',
  not_found: 'Image missing or private',
  blocked: 'Registry address blocked',
  rate_limited: 'Registry rate limit',
  error: 'Registry read failed',
  not_checked: 'Check limit reached',
  docker_hub_limit: 'Docker Hub lookup limit reached',
  java_home_declared: 'Image sets JAVA_HOME. Java version unknown',
  no_java_declaration: 'Image declares no Java version',
}

function Java4ShellResults({ data, checkedAt, onOpen, checkingAgain }: { data: Java4ShellResult; checkedAt: string; onOpen?: (resource: SelectedResource) => void; checkingAgain: boolean }) {
  const missingDeclaration = data.unassessed.no_java_declaration ?? 0
  const javaHomeDeclared = data.unassessed.java_home_declared ?? 0
  const dockerHubLimit = data.unassessed.docker_hub_limit ?? 0
  const checkLimitReached = data.unassessed.not_checked ?? 0
  const registryAuthRequired = data.unassessed.auth_required ?? 0
  const unassessed = Object.values(data.unassessed).reduce((count, n) => count + n, 0)
  const gaps = unassessed - missingDeclaration
  const assessedOutside = data.containers - data.matches - unassessed
  const remainingReasons = Object.entries(data.unassessed).filter(([reason]) =>
    !['auth_required', 'not_checked', 'docker_hub_limit', 'java_home_declared', 'no_java_declaration'].includes(reason),
  )
  const coverage = resourceCoverageNotice(data.coverage, 'Pods') ?? resourceNarrowedNotice(data.coverage)
  const namespaces = data.coverage.read?.namespaces
  const scope = data.workload?.name ?? (
    namespaces?.names && namespaces.names.length <= 3
      ? namespaces.names.join(', ')
      : namespaces ? pluralize(namespaces.count, 'namespace') : 'all namespaces'
  )
  const groups = new Map<string, Java4ShellFinding[]>()
  for (const row of data.findings) {
    const key = resourceRefKey(row.workload)
    const rows = groups.get(key)
    if (rows) rows.push(row)
    else groups.set(key, [row])
  }
  const summary = data.matches > 0
    ? `${data.matches} of ${data.matches + assessedOutside} assessed containers with an affected Java declaration`
    : data.containers === 0
      ? data.workload ? 'Radar found no running containers in this workload' : 'Radar found no running containers in the scope it read'
      : missingDeclaration === data.containers
        ? 'None of the running images declared a Java version'
        : unassessed === data.containers
          ? 'Radar could not assess the Java version of any running container'
          : `No affected declarations among ${pluralize(assessedOutside, 'assessed container')}`
  return (
    <div className="mt-3 flex flex-col gap-2 border-t border-theme-border pt-3 text-xs">
      <div className="flex flex-wrap justify-between gap-x-3 gap-y-1">
        <p role="status" aria-busy={checkingAgain} className="font-medium text-theme-text-primary">{summary}</p>
        <p className="text-theme-text-tertiary">{checkingAgain ? 'Previous result' : 'Checked'} <TimeValue timestamp={checkedAt} /> · {scope}</p>
      </div>
      {coverage && <p className="text-warning-text">{coverage}</p>}
      {data.containers > 0 && (
        <p className="text-theme-text-secondary">
          {pluralize(data.containers, 'running container')}
          {data.matches > 0 && assessedOutside > 0 && ` · ${assessedOutside} with versions outside affected ranges`}
          {gaps > 0 && ` · ${gaps} with assessment gaps`}
          {missingDeclaration > 0 && ` · ${missingDeclaration} with no Java version declared`}
        </p>
      )}
      {data.workload && <WorkloadJava4ShellRows data={data} />}
      {!data.workload && onOpen && data.findings.length > 0 && (
        <>
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
                          <WorkloadLink workload={row.workload} onOpen={onOpen} />
                        </th>
                      )}
                      <td className="px-3 py-2">
                        <div className="text-theme-text-primary">{row.container}</div>
                        <div className="max-w-48 truncate text-theme-text-tertiary"><ImageName image={row.image} /></div>
                        <div className="text-theme-text-tertiary">{pluralize(row.pods, 'Pod')}</div>
                      </td>
                      <td className="px-3 py-2">
                        <div className="font-mono text-theme-text-primary">{row.version}</div>
                        <div className="text-theme-text-secondary">{row.fixedIn ? `Advisory fix ${row.fixedIn}` : 'Check the vendor for an upgrade'}</div>
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
        <p className="text-theme-text-secondary">Showing {data.findings.length} of {data.findingGroups} matching groups.{!data.workload && ' Narrow the namespace scope to see more.'}</p>
      )}
      {data.findings.length > 0 && (
        <p className="text-theme-text-secondary">Confirm the installed Java version and the fix from its vendor.</p>
      )}
      {data.unaffectedByMajor && (
        <ul aria-label="Declared Java versions outside affected ranges" className="text-theme-text-secondary">
          {Object.entries(data.unaffectedByMajor).map(([major, count]) => (
            <li key={major}>Declared Java {major} outside affected versions · {pluralize(count, 'container')}</li>
          ))}
        </ul>
      )}
      {gaps > 0 && (
        <div className="space-y-1 border-t border-theme-border pt-2">
          <h3 className="font-medium text-theme-text-primary">Assessment gaps</h3>
          {registryAuthRequired > 0 && (
            <p className="text-warning-text">
              Registry authentication required for {pluralize(registryAuthRequired, 'container')}.{' '}
              <a className="text-accent-text hover:underline" href={registryAccessURL} target="_blank" rel="noreferrer">Registry credentials and permissions</a>
            </p>
          )}
          {checkLimitReached > 0 && <p className="text-warning-text">Check limit reached for {pluralize(checkLimitReached, 'container')}.</p>}
          {dockerHubLimit > 0 && <p className="text-warning-text">Docker Hub lookup limit reached for {pluralize(dockerHubLimit, 'container')}.</p>}
          {(checkLimitReached > 0 || dockerHubLimit > 0) && (
            <p className="text-theme-text-secondary">{data.workload ? 'Run the check again to retry.' : 'Select fewer namespaces or check a specific workload to retry.'}</p>
          )}
          {javaHomeDeclared > 0 && (
            <p className="text-theme-text-secondary">
              Images for {pluralize(javaHomeDeclared, 'container')} set <code>JAVA_HOME</code>. Verify the installed Java version.
            </p>
          )}
          {remainingReasons.map(([reason, count]) => (
            <p key={reason} className="text-theme-text-secondary">{reasonLabels[reason] ?? reason} · {pluralize(count, 'container')}</p>
          ))}
        </div>
      )}
      {data.unassessedGroups > 0 && <UnassessedDetails data={data} onOpen={onOpen} />}
    </div>
  )
}

function WorkloadJava4ShellRows({ data }: { data: Java4ShellResult }) {
  return (
    <>
      {data.findings.length > 0 && (
        <ul aria-label="Matching containers" className="divide-y divide-theme-border rounded-md border border-theme-border">
          {data.findings.map(row => (
            <li key={JSON.stringify([row.container, row.digest, row.version])} className="space-y-1 p-2">
              <div className="flex flex-wrap items-baseline gap-x-2">
                <span className="font-medium text-theme-text-primary">{row.container}</span>
                <ImageName image={row.image} className="text-theme-text-tertiary" />
                <span className="text-theme-text-tertiary">{pluralize(row.pods, 'Pod')}</span>
              </div>
              <div className="flex flex-wrap items-baseline gap-x-2">
                <span className="font-mono text-theme-text-primary">{row.version}</span>
                <span className="text-theme-text-secondary">{row.fixedIn ? `Advisory fix ${row.fixedIn}` : 'Check the vendor for an upgrade'}</span>
              </div>
              <JavaPlacement row={row} />
            </li>
          ))}
        </ul>
      )}
    </>
  )
}

function WorkloadLink({ workload, onOpen }: { workload: Java4ShellFinding['workload']; onOpen: (resource: SelectedResource) => void }) {
  return (
    <>
      <button type="button" onClick={() => onOpen(workload)} className="text-left text-accent-text hover:underline">{workload.name}</button>
      <div className="text-theme-text-tertiary">{workload.kind} · {workload.namespace}</div>
    </>
  )
}

function UnassessedDetails({ data, onOpen }: { data: Java4ShellResult; onOpen?: (resource: SelectedResource) => void }) {
  const [open, setOpen] = useState(false)
  const [showAll, setShowAll] = useState(false)
  const { panelId, buttonProps } = useDisclosure(open)
  const { unassessedRows, unassessedGroups } = data
  const gapRows = unassessedRows.filter(row => row.reason !== 'no_java_declaration')
  const canFilter = gapRows.length > 0 && gapRows.length < unassessedRows.length
  const rows = canFilter && !showAll ? gapRows : unassessedRows
  return (
    <div>
      <button {...buttonProps} type="button" onClick={() => setOpen(!open)} className="inline-flex items-center gap-1.5 py-1 font-medium text-accent-text hover:underline">
        <CollapseChevron open={open} className="h-3.5 w-3.5" />
        Containers without an assessment
      </button>
      <Collapse open={open} id={panelId} mountLazily>
        {canFilter && (
          <label className="mt-1 flex w-fit items-center gap-2 text-theme-text-secondary">
            <input type="checkbox" className="accent-accent" checked={showAll} onChange={event => setShowAll(event.target.checked)} />
            Include images with no Java version declaration
          </label>
        )}
        {data.workload ? (
          <ul aria-label="Unassessed containers" className="mt-1 divide-y divide-theme-border rounded-md border border-theme-border">
            {rows.map(row => (
              <li key={JSON.stringify([row.container, row.image, row.digest, row.reason])} className="space-y-1 p-2">
                <div className="flex flex-wrap items-baseline gap-x-2">
                  <span className="font-medium text-theme-text-primary">{row.container}</span>
                  <ImageName image={row.image} className="text-theme-text-tertiary" />
                  <span className="text-theme-text-tertiary">{pluralize(row.pods, 'Pod')}</span>
                </div>
                <div className="text-theme-text-secondary">{reasonLabels[row.reason] ?? row.reason}</div>
              </li>
            ))}
          </ul>
        ) : <div role="region" aria-label="Unassessed containers by workload" tabIndex={0} className="mt-1 max-h-64 overflow-auto rounded-lg border border-theme-border">
          <table className="w-full min-w-[32rem] text-left">
            <thead className="sticky top-0 bg-theme-elevated text-theme-text-secondary">
              <tr>
                <th scope="col" className="px-3 py-2 font-medium">Workload</th>
                <th scope="col" className="px-3 py-2 font-medium">Container</th>
                <th scope="col" className="px-3 py-2 font-medium">Reason</th>
                <th scope="col" className="px-3 py-2 text-right font-medium">Pods</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-theme-border border-t border-theme-border">
              {rows.map(row => (
                <tr key={JSON.stringify([resourceRefKey(row.workload), row.container, row.image, row.digest, row.reason])} className="align-top">
                  <th scope="row" className="px-3 py-2 font-medium">
                    {onOpen ? <WorkloadLink workload={row.workload} onOpen={onOpen} /> : (
                      <>
                        <div>{row.workload.name}</div>
                        <div className="text-theme-text-tertiary">{row.workload.kind} · {row.workload.namespace}</div>
                      </>
                    )}
                  </th>
                  <td className="px-3 py-2">
                    <div className="text-theme-text-primary">{row.container}</div>
                    <div className="max-w-48 truncate text-theme-text-tertiary"><ImageName image={row.image} /></div>
                    {row.digest && (
                      <Tooltip content={row.digest}>
                        <span className="font-mono text-theme-text-tertiary">{row.digest.replace(/^sha256:/, '').slice(0, 12)}</span>
                      </Tooltip>
                    )}
                  </td>
                  <td className="px-3 py-2 text-theme-text-secondary">{reasonLabels[row.reason] ?? row.reason}</td>
                  <td className="px-3 py-2 text-right tabular-nums text-theme-text-secondary">{row.pods}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>}
        {rows.length === unassessedRows.length && unassessedGroups > unassessedRows.length && (
          <p className="mt-2 text-theme-text-secondary">Showing {unassessedRows.length} of {unassessedGroups} groups.{!data.workload && ' Narrow the scope to see more.'}</p>
        )}
      </Collapse>
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
