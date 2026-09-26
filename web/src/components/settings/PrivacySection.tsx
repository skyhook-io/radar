import { useEffect, type ReactNode } from 'react'
import { ExternalLink } from 'lucide-react'
import { ConfigToggle, SubHeading } from './controls'
import { isRecording, useSetUsageData, useUsageData, type UsageDataStatus } from '../../api/usage-data'

const DOCS_URL = 'https://github.com/skyhook-io/radar/blob/main/docs/configuration.md#usage-data'

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
        <SubHeading>Usage data</SubHeading>
        <p className="text-xs text-theme-text-tertiary">
          Send anonymous usage stats to help improve Radar: counts of the views and actions you used
          and the MCP tools your agents called, how Radar is set up, and each cluster's minor
          version, platform, node count as a range and known integrations. It never includes names
          of resources, namespaces, clusters or hosts, or any contents, and reports carry no ID
          that links one to another.
        </p>
        <ConfigToggle
          label="Send anonymous usage stats"
          value={recording}
          disabled={!status.canChange || setUsageData.isPending}
          onChange={(v) => setUsageData.mutate(v)}
        />
        <ManagedReason status={status} />
        <SharedNote status={status} />
        <ReportPreview status={status} />
      </section>

      <a
        href={DOCS_URL}
        target="_blank"
        rel="noopener noreferrer"
        className="inline-flex items-center gap-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:underline"
      >
        How usage data works <ExternalLink className="w-3 h-3" />
      </a>
    </div>
  )
}

function ManagedReason({ status }: { status: UsageDataStatus }) {
  let text: string | null = null
  switch (status.source) {
    case 'do-not-track':
      text = 'Off because DO_NOT_TRACK is set.'
      break
    case 'env':
      text = status.state === 'log'
        ? 'Set by RADAR_USAGE_REPORTING=log: reports are written to Radar\'s log, never sent.'
        : status.shared
          ? `Set to ${status.state} for everyone by whoever installed Radar (Helm value usageReporting.enabled).`
          : `Set by RADAR_USAGE_REPORTING=${status.state}.`
      break
    case 'deployment':
      text = 'Radar Cloud manages usage data for this installation.'
      break
  }
  if (!text && status.developmentBuild && isRecording(status)) {
    text = 'This is a development build: reports are written to Radar\'s log, never sent.'
  }
  return text ? <p className="text-xs text-theme-text-tertiary">{text}</p> : null
}

// On a shared Radar the switch is the team's, so say so, who may change it,
// and who last did.
function SharedNote({ status }: { status: UsageDataStatus }) {
  const userDecided = status.source === 'default' || status.source === 'user'
  if (!status.shared || !userDecided) return null
  const when = status.decidedAt
    ? new Date(status.decidedAt).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
    : null
  const who = status.decidedBy
  const verb = status.state === 'on' ? 'Turned on' : 'Turned off'
  const how = status.preview.mode === 'in-cluster'
    ? <>the Helm value <code className="inline-code">usageReporting.enabled</code></>
    : <><code className="inline-code">RADAR_USAGE_REPORTING</code></>
  let whoCan: ReactNode
  if (status.canChange) {
    whoCan = 'This Radar is shared, so this switch applies to everyone who uses it.'
  } else if (status.ownersDecide) {
    whoCan = <>This Radar is shared, so only people who can change its Deployment can turn this on or off for everyone, in this switch or with {how}.</>
  } else {
    whoCan = <>This Radar is shared, so this is set for everyone with {how} by whoever runs it.</>
  }
  return (
    <p className="text-xs text-theme-text-tertiary">
      {whoCan}
      {when && (who ? ` ${verb} by ${who} on ${when}.` : ` ${verb} ${when}.`)}
      {status.canChange && status.choiceStorage === 'pod' &&
        " Radar can't save this choice in the cluster, so a restart resets it. Upgrading the Helm chart fixes that."}
    </p>
  )
}

function ReportPreview({ status }: { status: UsageDataStatus }) {
  const recording = isRecording(status)
  // Development builds and RADAR_USAGE_REPORTING=log write the report to the log
  // instead of sending it; say that rather than "sends".
  const logsOnly = status.state === 'log' || status.developmentBuild
  const next = status.nextReportAt ? new Date(status.nextReportAt) : null
  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-xs font-medium text-theme-text-secondary">
          {recording ? 'Next report' : 'What a report looks like'}
        </span>
        {recording && next && (
          <span className="text-xs text-theme-text-tertiary">
            {logsOnly ? 'Logs' : 'Sends'} {next.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}
          </span>
        )}
      </div>
      <pre className="max-h-80 overflow-auto rounded-md border border-theme-border bg-theme-elevated px-3 py-2 text-[11px] leading-relaxed font-mono text-theme-text-primary">
        {JSON.stringify(status.preview, null, 2)}
      </pre>
      <p className="text-xs text-theme-text-tertiary">
        {recording
          ? <>Kept in <code className="inline-code">~/.radar/usage-report.json</code> until it is {logsOnly ? 'written to the log' : 'sent'}. Turning this off deletes it.</>
          : 'Nothing is recorded or sent while this is off.'}
      </p>
    </div>
  )
}
