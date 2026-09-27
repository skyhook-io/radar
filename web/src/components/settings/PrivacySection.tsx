import { useEffect, useState } from 'react'
import { ExternalLink } from 'lucide-react'
import { Collapse, CollapseChevron, useDisclosure } from '@skyhook-io/k8s-ui/components/ui/Collapse'
import { ConfigToggle, SubHeading } from './controls'
import { isRecording, useSetUsageData, useUsageData, type UsageDataStatus } from '../../api/usage-data'

const DOCS_URL = 'https://radarhq.io/docs/configuration/usage-stats'

export function PrivacySection({ active }: { active: boolean }) {
  const { data: status, isLoading, error, refetch } = useUsageData(active, { fresh: true })
  const setUsageData = useSetUsageData()

  // The pane stays mounted while hidden, so refetch each time it is revealed.
  useEffect(() => {
    if (active) void refetch()
  }, [active, refetch])

  if (isLoading) {
    return <p className="text-sm text-theme-text-tertiary">Loading…</p>
  }
  if (error || !status) {
    return <p className="text-sm text-theme-text-secondary">Couldn't load privacy settings.</p>
  }

  const recording = isRecording(status)

  return (
    <div className="space-y-6">
      <p className="text-sm text-theme-text-secondary">
        {status.preview.mode === 'in-cluster' || status.preview.mode === 'cloud'
          ? 'Radar reads this cluster from inside it and keeps what it reads there.'
          : 'Radar reads your cluster from this machine and keeps what it reads here.'}
      </p>

      <section className="space-y-3">
        <SubHeading>Usage stats</SubHeading>
        <ConfigToggle
          label="Send anonymous usage stats"
          description={<>Shows us which features get used, so we know what to build next.<br />Sent straight to us, with no third-party trackers.</>}
          value={recording}
          disabled={!status.canChange || setUsageData.isPending}
          onChange={(v) => setUsageData.mutate(v)}
        />
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1.5 text-xs">
          <dt className="text-theme-text-tertiary">What's sent</dt>
          <dd className="text-theme-text-secondary">
            The views you open, the actions and MCP tools you use, and each cluster's version,
            platform, size range and known integrations.
          </dd>
          <dt className="text-theme-text-tertiary">What isn't</dt>
          <dd className="text-theme-text-secondary">
            Names, contents, logs, identifiers, timestamps, or the resources in your clusters.
          </dd>
        </dl>
        <ManagedReason status={status} />
        <SharedNote status={status} />
        <ReportPreview status={status} />
      </section>

    </div>
  )
}

function ManagedReason({ status }: { status: UsageDataStatus }) {
  let text: string | null = null
  switch (status.source) {
    case 'env':
      text = status.shared && status.preview.mode === 'in-cluster'
        ? 'Set for everyone by the Helm value usageReporting.enabled.'
        : `Set by RADAR_USAGE_REPORTING=${status.state}.`
      break
    case 'deployment':
      text = 'Radar Cloud manages usage data for this installation.'
      break
  }
  if (!text && status.developmentBuild && isRecording(status)) {
    text = 'Development build: reports go to the log, never sent.'
  }
  return text ? <p className="text-xs text-theme-text-tertiary">{text}</p> : null
}

// A shared Radar is decided by its configuration; say where, while nothing set it.
function SharedNote({ status }: { status: UsageDataStatus }) {
  if (!status.shared || status.source !== 'default') return null
  return (
    <p className="text-xs text-theme-text-tertiary">
      Applies to everyone using this Radar. Set it with{' '}
      {status.preview.mode === 'in-cluster'
        ? <>the Helm value <code className="inline-code">usageReporting.enabled</code></>
        : <code className="inline-code">RADAR_USAGE_REPORTING=on</code>}.
    </p>
  )
}

function ReportPreview({ status }: { status: UsageDataStatus }) {
  const recording = isRecording(status)
  const [open, setOpen] = useState(false)
  const disclosure = useDisclosure(open)
  // Development builds write the report to the log instead of sending it.
  const logsOnly = status.developmentBuild
  const next = status.nextReportAt ? new Date(status.nextReportAt) : null
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-3">
        <button
          type="button"
          onClick={() => setOpen(o => !o)}
          {...disclosure.buttonProps}
          className="flex items-center gap-1 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary"
        >
          <CollapseChevron open={open} />
          {recording ? 'See the next report' : 'See an example report'}
          {recording && next && (
            <span className="font-normal text-theme-text-tertiary">
              {' '}· {logsOnly ? 'logs' : 'sends'} {next.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}
            </span>
          )}
        </button>
        <a
          href={DOCS_URL}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:underline"
        >
          How it works <ExternalLink className="w-3 h-3" aria-hidden />
        </a>
      </div>
      <Collapse open={open} id={disclosure.panelId}>
        <pre className="max-h-80 overflow-auto rounded-md border border-theme-border bg-theme-elevated px-3 py-2 text-[11px] leading-relaxed font-mono text-theme-text-primary">
          {JSON.stringify(status.preview, null, 2)}
        </pre>
      </Collapse>
      {recording && status.canChange && (
        <p className="text-xs text-theme-text-tertiary">
          Waiting in <code className="inline-code">~/.radar/usage-report.json</code>. Turning this off deletes it.
        </p>
      )}
    </div>
  )
}
