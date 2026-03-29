import { Shield, ShieldCheck, ShieldAlert, FileWarning, ListChecks, ChevronDown, ChevronRight } from 'lucide-react'
import { clsx } from 'clsx'
import { useState } from 'react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import {
  getPolicyReportSummary,
  getPolicyReportResults,
  getPolicyReportScope,
  getPolicyReportSource,
  getKyvernoPolicyAction,
  getKyvernoPolicyRuleCount,
  getKyvernoPolicyBackground,
  getKyvernoPolicyRules,
  getKyvernoPolicyRuleCountByType,
  getKyvernoPolicyAutogenRules,
} from '../resource-utils-kyverno'

// ============================================================================
// PolicyReport / ClusterPolicyReport Renderer
// ============================================================================

interface PolicyReportRendererProps {
  data: any
}

const resultColorMap: Record<string, string> = {
  pass: 'bg-green-500/20 text-green-400',
  fail: 'bg-red-500/20 text-red-400',
  warn: 'bg-yellow-500/20 text-yellow-400',
  error: 'bg-red-500/20 text-red-400',
  skip: 'bg-blue-500/20 text-blue-400',
}

const severityColorMap: Record<string, string> = {
  critical: 'bg-red-500/20 text-red-400',
  high: 'bg-orange-500/20 text-orange-400',
  medium: 'bg-yellow-500/20 text-yellow-400',
  low: 'bg-blue-500/20 text-blue-400',
  info: 'bg-theme-hover text-theme-text-tertiary',
}

function ResultRow({ result }: { result: any }) {
  const [expanded, setExpanded] = useState(false)
  const message = result.message || ''
  const hasMessage = message.length > 0

  return (
    <div className="border-b border-theme-border/30 last:border-b-0">
      <div
        className={clsx(
          'flex items-center gap-2 py-1.5 px-2 text-xs',
          hasMessage && 'cursor-pointer hover:bg-theme-hover/50',
        )}
        onClick={() => hasMessage && setExpanded(!expanded)}
      >
        {hasMessage ? (
          expanded ? <ChevronDown className="w-3 h-3 text-theme-text-tertiary shrink-0" /> : <ChevronRight className="w-3 h-3 text-theme-text-tertiary shrink-0" />
        ) : (
          <span className="w-3 shrink-0" />
        )}
        {/* Result status */}
        <span className={clsx('px-1.5 py-0.5 rounded text-[10px] font-medium shrink-0', resultColorMap[result.result] || 'bg-theme-hover text-theme-text-tertiary')}>
          {result.result || '-'}
        </span>
        {/* Severity */}
        {result.severity && (
          <span className={clsx('px-1.5 py-0.5 rounded text-[10px] font-medium shrink-0', severityColorMap[result.severity?.toLowerCase()] || 'bg-theme-hover text-theme-text-tertiary')}>
            {result.severity}
          </span>
        )}
        {/* Policy / Rule */}
        <span className="text-theme-text-secondary truncate">{result.policy || '-'}</span>
        {result.rule && (
          <span className="text-theme-text-tertiary truncate">/ {result.rule}</span>
        )}
      </div>
      {expanded && message && (
        <div className="px-2 pb-2 pl-7">
          <div className="text-xs text-theme-text-secondary break-all card-inner">
            {message}
          </div>
          {result.category && (
            <div className="mt-1 text-[10px] text-theme-text-tertiary">Category: {result.category}</div>
          )}
          {result.source && (
            <div className="text-[10px] text-theme-text-tertiary">Source: {result.source}</div>
          )}
          {result.resources && result.resources.length > 0 && (
            <div className="mt-1 text-[10px] text-theme-text-tertiary">
              Resources: {result.resources.map((r: any) => `${r.kind}/${r.namespace ? r.namespace + '/' : ''}${r.name}`).join(', ')}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export function PolicyReportRenderer({ data }: PolicyReportRendererProps) {
  const summary = getPolicyReportSummary(data)
  const results = getPolicyReportResults(data)
  const scope = getPolicyReportScope(data)
  const source = getPolicyReportSource(data)
  const total = summary.pass + summary.fail + summary.warn + summary.error + summary.skip

  const failResults = results.filter((r: any) => r.result === 'fail')
  const errorResults = results.filter((r: any) => r.result === 'error')

  return (
    <>
      {/* Problem alerts */}
      {summary.fail > 0 && (
        <AlertBanner
          variant="error"
          title={`${summary.fail} policy check${summary.fail > 1 ? 's' : ''} failed`}
          message={failResults.length <= 3
            ? failResults.map((r: any) => `${r.policy}${r.rule ? '/' + r.rule : ''}: ${r.message || 'failed'}`).join('; ')
            : `${failResults.slice(0, 2).map((r: any) => r.policy).join(', ')} and ${failResults.length - 2} more`
          }
        />
      )}
      {summary.error > 0 && summary.fail === 0 && (
        <AlertBanner
          variant="error"
          title={`${summary.error} policy check${summary.error > 1 ? 's' : ''} errored`}
          message={errorResults.length <= 3
            ? errorResults.map((r: any) => `${r.policy}: ${r.message || 'error'}`).join('; ')
            : undefined
          }
        />
      )}
      {summary.warn > 0 && summary.fail === 0 && summary.error === 0 && (
        <AlertBanner
          variant="warning"
          title={`${summary.warn} policy warning${summary.warn > 1 ? 's' : ''}`}
        />
      )}

      {/* Summary */}
      <Section title="Summary" icon={ShieldCheck}>
        <PropertyList>
          {scope !== '-' && <Property label="Scope" value={scope} />}
          {source !== '-' && <Property label="Source" value={source} />}
        </PropertyList>

        {/* Visual summary bar */}
        {total > 0 && (
          <div className="mt-3">
            <div className="flex rounded overflow-hidden h-5 w-full">
              {summary.pass > 0 && (
                <div
                  className="bg-green-500/60 flex items-center justify-center text-[10px] font-medium text-white"
                  style={{ width: `${(summary.pass / total) * 100}%` }}
                  title={`Pass: ${summary.pass}`}
                >
                  {summary.pass > 0 && total > 3 ? summary.pass : ''}
                </div>
              )}
              {summary.fail > 0 && (
                <div
                  className="bg-red-500/60 flex items-center justify-center text-[10px] font-medium text-white"
                  style={{ width: `${(summary.fail / total) * 100}%` }}
                  title={`Fail: ${summary.fail}`}
                >
                  {summary.fail > 0 && total > 3 ? summary.fail : ''}
                </div>
              )}
              {summary.warn > 0 && (
                <div
                  className="bg-yellow-500/60 flex items-center justify-center text-[10px] font-medium text-white"
                  style={{ width: `${(summary.warn / total) * 100}%` }}
                  title={`Warn: ${summary.warn}`}
                >
                  {summary.warn > 0 && total > 3 ? summary.warn : ''}
                </div>
              )}
              {summary.error > 0 && (
                <div
                  className="bg-red-400/60 flex items-center justify-center text-[10px] font-medium text-white"
                  style={{ width: `${(summary.error / total) * 100}%` }}
                  title={`Error: ${summary.error}`}
                >
                  {summary.error > 0 && total > 3 ? summary.error : ''}
                </div>
              )}
              {summary.skip > 0 && (
                <div
                  className="bg-blue-500/40 flex items-center justify-center text-[10px] font-medium text-white"
                  style={{ width: `${(summary.skip / total) * 100}%` }}
                  title={`Skip: ${summary.skip}`}
                >
                  {summary.skip > 0 && total > 3 ? summary.skip : ''}
                </div>
              )}
            </div>
            <div className="flex gap-3 mt-1.5 text-[10px]">
              {summary.pass > 0 && <span className="text-green-400">Pass: {summary.pass}</span>}
              {summary.fail > 0 && <span className="text-red-400">Fail: {summary.fail}</span>}
              {summary.warn > 0 && <span className="text-yellow-400">Warn: {summary.warn}</span>}
              {summary.error > 0 && <span className="text-red-400">Error: {summary.error}</span>}
              {summary.skip > 0 && <span className="text-blue-400">Skip: {summary.skip}</span>}
            </div>
          </div>
        )}
      </Section>

      {/* Results */}
      {results.length > 0 && (
        <Section title={`Results (${results.length})`} icon={ListChecks} defaultExpanded>
          <div className="bg-theme-elevated/20 rounded-lg overflow-hidden max-h-[500px] overflow-y-auto">
            {results.map((result: any, i: number) => (
              <ResultRow key={i} result={result} />
            ))}
          </div>
        </Section>
      )}
    </>
  )
}

// ============================================================================
// Kyverno Policy / ClusterPolicy Renderer
// ============================================================================

interface KyvernoPolicyRendererProps {
  data: any
}

const ruleTypeColorMap: Record<string, string> = {
  validate: 'bg-blue-500/20 text-blue-400',
  mutate: 'bg-purple-500/20 text-purple-400',
  generate: 'bg-green-500/20 text-green-400',
  verifyImages: 'bg-orange-500/20 text-orange-400',
}

export function KyvernoPolicyRenderer({ data }: KyvernoPolicyRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const conditions = status.conditions || []
  const action = getKyvernoPolicyAction(data)
  const ruleCount = getKyvernoPolicyRuleCount(data)
  const background = getKyvernoPolicyBackground(data)
  const rules = getKyvernoPolicyRules(data)
  const ruleCountByType = getKyvernoPolicyRuleCountByType(data)
  const autogenRules = getKyvernoPolicyAutogenRules(data)

  const isEnforce = action === 'Enforce'

  return (
    <>
      {/* Configuration */}
      <Section title="Configuration" icon={Shield}>
        <PropertyList>
          <Property label="Failure Action" value={
            <span className={clsx(
              'badge',
              isEnforce ? 'bg-red-500/20 text-red-400' : 'bg-yellow-500/20 text-yellow-400',
            )}>
              {action}
            </span>
          } />
          <Property label="Background" value={background ? 'Enabled' : 'Disabled'} />
          {spec.webhookTimeoutSeconds && (
            <Property label="Webhook Timeout" value={`${spec.webhookTimeoutSeconds}s`} />
          )}
          {spec.failurePolicy && (
            <Property label="Failure Policy" value={spec.failurePolicy} />
          )}
          {spec.schemaValidation !== undefined && (
            <Property label="Schema Validation" value={spec.schemaValidation ? 'Enabled' : 'Disabled'} />
          )}
        </PropertyList>

        {/* Rule count summary */}
        <div className="mt-3 flex flex-wrap gap-2">
          {ruleCountByType.validate > 0 && (
            <span className="px-2 py-0.5 rounded text-[10px] font-medium bg-blue-500/20 text-blue-400">
              {ruleCountByType.validate} validate
            </span>
          )}
          {ruleCountByType.mutate > 0 && (
            <span className="px-2 py-0.5 rounded text-[10px] font-medium bg-purple-500/20 text-purple-400">
              {ruleCountByType.mutate} mutate
            </span>
          )}
          {ruleCountByType.generate > 0 && (
            <span className="px-2 py-0.5 rounded text-[10px] font-medium bg-green-500/20 text-green-400">
              {ruleCountByType.generate} generate
            </span>
          )}
          {ruleCountByType.verifyImages > 0 && (
            <span className="px-2 py-0.5 rounded text-[10px] font-medium bg-orange-500/20 text-orange-400">
              {ruleCountByType.verifyImages} verifyImages
            </span>
          )}
        </div>
      </Section>

      {/* Rules */}
      {ruleCount > 0 && (
        <Section title={`Rules (${ruleCount})`} icon={FileWarning} defaultExpanded>
          <div className="space-y-2">
            {rules.map((rule, i) => (
              <div key={i} className="card-inner-lg">
                <div className="flex items-center gap-2 mb-1">
                  <span className="text-sm font-medium text-theme-text-primary">{rule.name}</span>
                  <span className={clsx('px-1.5 py-0.5 rounded text-[10px] font-medium', ruleTypeColorMap[rule.type] || 'bg-theme-hover text-theme-text-tertiary')}>
                    {rule.type}
                  </span>
                </div>
                <div className="flex gap-3 text-[10px] text-theme-text-tertiary">
                  {rule.hasMatch && <span>match</span>}
                  {rule.hasExclude && <span>exclude</span>}
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Autogenerated Rules */}
      {autogenRules.length > 0 && (
        <Section title={`Autogenerated Rules (${autogenRules.length})`} defaultExpanded={false}>
          <div className="flex flex-wrap gap-1">
            {autogenRules.map((name: string) => (
              <span
                key={name}
                className="badge bg-theme-elevated text-theme-text-secondary"
              >
                {name}
              </span>
            ))}
          </div>
        </Section>
      )}

      {/* Status rulecount from Kyverno controller */}
      {status.rulecount && (
        <Section title="Status Rule Count" icon={ShieldAlert} defaultExpanded={false}>
          <PropertyList>
            {status.rulecount.validate !== undefined && <Property label="Validate" value={status.rulecount.validate} />}
            {status.rulecount.mutate !== undefined && <Property label="Mutate" value={status.rulecount.mutate} />}
            {status.rulecount.generate !== undefined && <Property label="Generate" value={status.rulecount.generate} />}
            {status.rulecount.verifyImages !== undefined && <Property label="Verify Images" value={status.rulecount.verifyImages} />}
          </PropertyList>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
