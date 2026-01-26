import { useState, useCallback, useEffect, useMemo, useRef } from 'react'
import { X, Package, ChevronRight, ChevronLeft, Play, Loader2, AlertTriangle, CheckCircle, User, BookOpen, Link as LinkIcon, Star, BadgeCheck, Shield, Globe, Building2, Plus, Minus, Terminal } from 'lucide-react'
import { clsx } from 'clsx'
import yaml from 'yaml'
import { createPatch } from 'diff'
import { useQueryClient } from '@tanstack/react-query'
import { useChartDetail, useNamespaces, useArtifactHubChart, installChartWithProgress, type InstallProgressEvent } from '../../api/client'
import type { ChartSource, ChartDetail, ArtifactHubChartDetail } from '../../types'
import { YamlEditor } from '../ui/YamlEditor'
import { Tooltip } from '../ui/Tooltip'
import { Markdown } from '../ui/Markdown'

interface InstallWizardProps {
  repo: string
  chartName: string
  version: string
  source: ChartSource
  repoUrl?: string  // Direct repo URL (overrides ArtifactHub lookup)
  onClose: () => void
  onSuccess: (namespace: string, releaseName: string) => void
}

type WizardStep = 'info' | 'values' | 'review' | 'installing'

// Progress log entry
interface ProgressEntry {
  phase: string
  message: string
  detail?: string
  timestamp: Date
}

export function InstallWizard({ repo, chartName, version, source, repoUrl, onClose, onSuccess }: InstallWizardProps) {
  const [step, setStep] = useState<WizardStep>('info')
  const [releaseName, setReleaseName] = useState(chartName)
  const [namespace, setNamespace] = useState(chartName)
  const [createNamespace, setCreateNamespace] = useState(true)
  const [valuesYaml, setValuesYaml] = useState('')
  const [yamlError, setYamlError] = useState<string | null>(null)
  const [showReadme, setShowReadme] = useState(false)

  // Install progress state
  const [progressLogs, setProgressLogs] = useState<ProgressEntry[]>([])
  const [installError, setInstallError] = useState<string | null>(null)
  const [isInstalling, setIsInstalling] = useState(false)
  const progressEndRef = useRef<HTMLDivElement>(null)

  const queryClient = useQueryClient()

  // Choose the right data based on source
  const isLocal = source === 'local'

  // Use appropriate hook based on source - only enable the relevant one
  const { data: localChartDetail, isLoading: localChartLoading } = useChartDetail(
    repo, chartName, version, isLocal
  )
  const { data: artifactHubDetail, isLoading: artifactHubLoading } = useArtifactHubChart(
    repo, chartName, version, !isLocal
  )

  const chartLoading = isLocal ? localChartLoading : artifactHubLoading
  const chartDetail = isLocal ? localChartDetail : artifactHubDetail

  const { data: namespaces } = useNamespaces()

  // Auto-scroll progress logs
  useEffect(() => {
    if (progressEndRef.current) {
      progressEndRef.current.scrollIntoView({ behavior: 'smooth' })
    }
  }, [progressLogs])

  // Initialize values from chart defaults
  useEffect(() => {
    if (isLocal && localChartDetail?.values) {
      setValuesYaml(yaml.stringify(localChartDetail.values))
    } else if (!isLocal && artifactHubDetail?.values) {
      // ArtifactHub returns values as a string
      setValuesYaml(artifactHubDetail.values)
    }
  }, [localChartDetail?.values, artifactHubDetail?.values, isLocal])

  const handleInstall = useCallback(async () => {
    let values: Record<string, unknown> | undefined
    if (valuesYaml.trim()) {
      try {
        values = yaml.parse(valuesYaml)
      } catch (err) {
        setYamlError(err instanceof Error ? err.message : 'Invalid YAML')
        return
      }
    }

    // For ArtifactHub charts, we need to use the repository URL
    // If repoUrl is provided directly, use it (for non-ArtifactHub external repos)
    const repository = repoUrl || (isLocal ? repo : (artifactHubDetail?.repository.url || repo))

    // Switch to installing step
    setStep('installing')
    setIsInstalling(true)
    setInstallError(null)
    setProgressLogs([])

    try {
      const release = await installChartWithProgress(
        {
          releaseName,
          namespace,
          chartName,
          version,
          repository,
          values,
          createNamespace,
        },
        (event: InstallProgressEvent) => {
          if (event.type === 'progress' && event.message) {
            setProgressLogs(prev => [...prev, {
              phase: event.phase || 'progress',
              message: event.message || '',
              detail: event.detail,
              timestamp: new Date(),
            }])
          }
        }
      )

      // Success - add final log entry
      setProgressLogs(prev => [...prev, {
        phase: 'complete',
        message: `Successfully installed ${release.name}`,
        timestamp: new Date(),
      }])

      // Invalidate queries
      queryClient.invalidateQueries({ queryKey: ['helm-releases'] })

      // Wait a moment to show success, then close
      setTimeout(() => {
        onSuccess(namespace, releaseName)
      }, 1500)
    } catch (err) {
      setInstallError(err instanceof Error ? err.message : 'Install failed')
      setProgressLogs(prev => [...prev, {
        phase: 'error',
        message: err instanceof Error ? err.message : 'Install failed',
        timestamp: new Date(),
      }])
    } finally {
      setIsInstalling(false)
    }
  }, [releaseName, namespace, chartName, version, repo, valuesYaml, createNamespace, onSuccess, isLocal, artifactHubDetail, queryClient])

  const canProceedFromInfo = releaseName.trim() !== '' && namespace.trim() !== ''
  const canInstall = canProceedFromInfo && !yamlError

  const steps: { id: WizardStep; label: string }[] = [
    { id: 'info', label: 'Details' },
    { id: 'values', label: 'Values' },
    { id: 'review', label: 'Review' },
  ]

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      {/* Backdrop */}
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />

      {/* Dialog */}
      <div className="relative bg-theme-surface border border-theme-border rounded-lg shadow-2xl max-w-3xl w-full mx-4 max-h-[90vh] flex flex-col">
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-theme-border shrink-0">
          <div className="flex items-center gap-3">
            {(isLocal ? (chartDetail as ChartDetail)?.icon : (chartDetail as ArtifactHubChartDetail)?.logoUrl) ? (
              <img
                src={isLocal ? (chartDetail as ChartDetail)?.icon : (chartDetail as ArtifactHubChartDetail)?.logoUrl}
                alt=""
                className="w-8 h-8 rounded object-contain bg-white/10 p-1"
              />
            ) : (
              <Package className="w-8 h-8 text-purple-400" />
            )}
            <div>
              <div className="flex items-center gap-2">
                <h2 className="text-lg font-semibold text-theme-text-primary">Install {chartName}</h2>
                {!isLocal && (
                  <Tooltip content="From ArtifactHub">
                    <Globe className="w-4 h-4 text-blue-400" />
                  </Tooltip>
                )}
              </div>
              <div className="flex items-center gap-2 text-sm text-theme-text-tertiary">
                <span>{repo} / {version}</span>
                {!isLocal && (chartDetail as ArtifactHubChartDetail)?.repository?.official && (
                  <Tooltip content="Official">
                    <BadgeCheck className="w-3.5 h-3.5 text-blue-400" />
                  </Tooltip>
                )}
                {!isLocal && (chartDetail as ArtifactHubChartDetail)?.repository?.verifiedPublisher && (
                  <Tooltip content="Verified Publisher">
                    <Shield className="w-3.5 h-3.5 text-green-400" />
                  </Tooltip>
                )}
              </div>
            </div>
          </div>
          <button
            onClick={onClose}
            className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Step indicator - hidden during install */}
        {step !== 'installing' && (
          <div className="flex items-center justify-center gap-2 px-4 py-3 border-b border-theme-border bg-theme-surface/50">
            {steps.map((s, i) => (
              <div key={s.id} className="flex items-center">
                {i > 0 && <ChevronRight className="w-4 h-4 text-theme-text-disabled mx-2" />}
                <button
                  onClick={() => {
                    if (s.id === 'info' || (s.id === 'values' && canProceedFromInfo) || (s.id === 'review' && canInstall)) {
                      setStep(s.id)
                    }
                  }}
                  className={clsx(
                    'flex items-center gap-2 px-3 py-1.5 rounded-full text-sm transition-colors',
                    step === s.id
                      ? 'bg-blue-500/20 text-blue-400'
                      : 'text-theme-text-secondary hover:text-theme-text-primary'
                  )}
                >
                  <span className={clsx(
                    'w-5 h-5 rounded-full flex items-center justify-center text-xs font-medium',
                    step === s.id ? 'bg-blue-500 text-white' : 'bg-theme-elevated text-theme-text-secondary'
                  )}>
                    {i + 1}
                  </span>
                  {s.label}
                </button>
              </div>
            ))}
          </div>
        )}

        {/* Content */}
        <div className="flex-1 overflow-auto p-4">
          {chartLoading ? (
            <div className="flex items-center justify-center h-32 text-theme-text-tertiary">
              <Loader2 className="w-5 h-5 animate-spin mr-2" />
              Loading chart details...
            </div>
          ) : (
            <>
              {step === 'info' && (
                <InfoStep
                  chartDetail={chartDetail}
                  source={source}
                  releaseName={releaseName}
                  setReleaseName={setReleaseName}
                  namespace={namespace}
                  setNamespace={setNamespace}
                  namespaces={namespaces || []}
                  createNamespace={createNamespace}
                  setCreateNamespace={setCreateNamespace}
                  showReadme={showReadme}
                  setShowReadme={setShowReadme}
                />
              )}

              {step === 'values' && (
                <ValuesStep
                  valuesYaml={valuesYaml}
                  setValuesYaml={setValuesYaml}
                  yamlError={yamlError}
                  setYamlError={setYamlError}
                  chartDetail={chartDetail}
                  source={source}
                />
              )}

              {step === 'review' && (
                <ReviewStep
                  releaseName={releaseName}
                  namespace={namespace}
                  chartName={chartName}
                  version={version}
                  repo={repo}
                  source={source}
                  artifactHubRepoUrl={!isLocal ? (chartDetail as ArtifactHubChartDetail)?.repository?.url : undefined}
                  createNamespace={createNamespace}
                  valuesYaml={valuesYaml}
                  defaultValuesYaml={
                    isLocal
                      ? (localChartDetail?.values ? yaml.stringify(localChartDetail.values) : '')
                      : (artifactHubDetail?.values || '')
                  }
                />
              )}

              {step === 'installing' && (
                <InstallingStep
                  releaseName={releaseName}
                  namespace={namespace}
                  chartName={chartName}
                  progressLogs={progressLogs}
                  isInstalling={isInstalling}
                  installError={installError}
                  progressEndRef={progressEndRef}
                />
              )}
            </>
          )}
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between px-4 py-3 border-t border-theme-border shrink-0">
          <div>
            {step !== 'info' && step !== 'installing' && (
              <button
                onClick={() => setStep(step === 'review' ? 'values' : 'info')}
                className="flex items-center gap-1 px-4 py-2 text-sm text-theme-text-secondary hover:text-theme-text-primary transition-colors"
              >
                <ChevronLeft className="w-4 h-4" />
                Back
              </button>
            )}
          </div>
          <div className="flex items-center gap-3">
            {step === 'installing' ? (
              // Installing step - only show close if done or error
              <>
                {!isInstalling && installError && (
                  <button
                    onClick={() => setStep('review')}
                    className="flex items-center gap-1 px-4 py-2 text-sm text-theme-text-secondary hover:text-theme-text-primary transition-colors"
                  >
                    <ChevronLeft className="w-4 h-4" />
                    Back to Review
                  </button>
                )}
                {isInstalling && (
                  <span className="text-sm text-theme-text-tertiary">
                    Installing...
                  </span>
                )}
              </>
            ) : (
              <>
                <button
                  onClick={onClose}
                  className="px-4 py-2 text-sm text-theme-text-secondary hover:text-theme-text-primary transition-colors"
                >
                  Cancel
                </button>
                {step === 'review' ? (
                  <button
                    onClick={handleInstall}
                    disabled={!canInstall || isInstalling}
                    className="flex items-center gap-2 px-4 py-2 text-sm font-medium text-white bg-blue-600 hover:bg-blue-700 rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    {isInstalling ? (
                      <Loader2 className="w-4 h-4 animate-spin" />
                    ) : (
                      <Play className="w-4 h-4" />
                    )}
                    Install
                  </button>
                ) : (
                  <button
                    onClick={() => setStep(step === 'info' ? 'values' : 'review')}
                    disabled={step === 'info' && !canProceedFromInfo}
                    className="flex items-center gap-1 px-4 py-2 text-sm font-medium text-white bg-blue-600 hover:bg-blue-700 rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    Next
                    <ChevronRight className="w-4 h-4" />
                  </button>
                )}
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

interface InfoStepProps {
  chartDetail: ChartDetail | ArtifactHubChartDetail | undefined
  source: ChartSource
  releaseName: string
  setReleaseName: (name: string) => void
  namespace: string
  setNamespace: (ns: string) => void
  namespaces: { name: string }[]
  createNamespace: boolean
  setCreateNamespace: (create: boolean) => void
  showReadme: boolean
  setShowReadme: (show: boolean) => void
}

function InfoStep({
  chartDetail,
  source,
  releaseName,
  setReleaseName,
  namespace,
  setNamespace,
  namespaces,
  createNamespace,
  setCreateNamespace,
  showReadme,
  setShowReadme,
}: InfoStepProps) {
  const isLocal = source === 'local'
  const localDetail = chartDetail as ChartDetail | undefined
  const ahDetail = chartDetail as ArtifactHubChartDetail | undefined

  const description = isLocal ? localDetail?.description : ahDetail?.description
  const home = isLocal ? localDetail?.home : ahDetail?.homeUrl
  const maintainers = isLocal ? localDetail?.maintainers : ahDetail?.maintainers
  const readme = isLocal ? localDetail?.readme : ahDetail?.readme

  return (
    <div className="space-y-6">
      {/* ArtifactHub specific metadata */}
      {!isLocal && ahDetail && (
        <div className="flex items-center gap-4 p-3 bg-theme-elevated/30 rounded-lg">
          {ahDetail.stars > 0 && (
            <Tooltip content={`${ahDetail.stars} stars`}>
              <span className="flex items-center gap-1 text-sm text-amber-400">
                <Star className="w-4 h-4 fill-current" />
                {ahDetail.stars > 999 ? `${(ahDetail.stars / 1000).toFixed(1)}k` : ahDetail.stars}
              </span>
            </Tooltip>
          )}
          {ahDetail.productionOrgsCount && ahDetail.productionOrgsCount > 0 && (
            <Tooltip content={`Used in ${ahDetail.productionOrgsCount} production environments`}>
              <span className="flex items-center gap-1 text-sm text-theme-text-secondary">
                <Building2 className="w-4 h-4" />
                {ahDetail.productionOrgsCount} orgs
              </span>
            </Tooltip>
          )}
          {ahDetail.security && (ahDetail.security.critical || ahDetail.security.high) && (
            <Tooltip content={`Security: ${ahDetail.security.critical || 0} critical, ${ahDetail.security.high || 0} high`}>
              <span className={clsx(
                'flex items-center gap-1 text-sm',
                ahDetail.security.critical ? 'text-red-400' : 'text-orange-400'
              )}>
                <Shield className="w-4 h-4" />
                {(ahDetail.security.critical || 0) + (ahDetail.security.high || 0)} issues
              </span>
            </Tooltip>
          )}
          {ahDetail.signed && (
            <span className="flex items-center gap-1 text-sm text-green-400">
              <BadgeCheck className="w-4 h-4" />
              Signed
            </span>
          )}
          {ahDetail.license && (
            <span className="text-sm text-theme-text-tertiary">{ahDetail.license}</span>
          )}
        </div>
      )}

      {/* Chart description */}
      {description && (
        <div className="bg-theme-elevated/30 rounded-lg p-4">
          <p className="text-sm text-theme-text-secondary">{description}</p>
          {(home || maintainers?.length) && (
            <div className="flex flex-wrap gap-4 mt-3 pt-3 border-t border-theme-border">
              {home && (
                <a
                  href={home}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="flex items-center gap-1 text-xs text-blue-400 hover:text-blue-300"
                >
                  <LinkIcon className="w-3.5 h-3.5" />
                  Homepage
                </a>
              )}
              {maintainers && maintainers.length > 0 && (
                <span className="flex items-center gap-1 text-xs text-theme-text-tertiary">
                  <User className="w-3.5 h-3.5" />
                  {maintainers.map((m) => m.name).join(', ')}
                </span>
              )}
            </div>
          )}
        </div>
      )}

      {/* Release name input */}
      <div>
        <label className="block text-sm font-medium text-theme-text-secondary mb-2">
          Release Name
        </label>
        <input
          type="text"
          value={releaseName}
          onChange={(e) => setReleaseName(e.target.value)}
          placeholder="my-release"
          className="w-full px-3 py-2 bg-theme-elevated border border-theme-border-light rounded-lg text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
        <p className="mt-1 text-xs text-theme-text-tertiary">
          A unique name for this release in the namespace
        </p>
      </div>

      {/* Namespace selection */}
      <div>
        <label className="block text-sm font-medium text-theme-text-secondary mb-2">
          Namespace
        </label>
        <input
          type="text"
          list="namespace-suggestions"
          value={namespace}
          onChange={(e) => setNamespace(e.target.value)}
          placeholder="Enter namespace name"
          className="w-full px-3 py-2 bg-theme-elevated border border-theme-border-light rounded-lg text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
        <datalist id="namespace-suggestions">
          {namespaces.map(ns => (
            <option key={ns.name} value={ns.name} />
          ))}
        </datalist>
        <label className="flex items-center gap-2 mt-2 text-sm text-theme-text-secondary cursor-pointer">
          <input
            type="checkbox"
            checked={createNamespace}
            onChange={(e) => setCreateNamespace(e.target.checked)}
            className="rounded border-theme-border text-blue-500 focus:ring-blue-500"
          />
          Create namespace if it doesn't exist
        </label>
      </div>

      {/* README toggle */}
      {readme && (
        <div>
          <button
            onClick={() => setShowReadme(!showReadme)}
            className="flex items-center gap-2 text-sm text-blue-400 hover:text-blue-300"
          >
            <BookOpen className="w-4 h-4" />
            {showReadme ? 'Hide' : 'Show'} Chart README
          </button>
          {showReadme && (
            <div className="mt-3 p-4 bg-theme-base/50 rounded-lg text-xs overflow-auto max-h-96">
              <Markdown>{readme}</Markdown>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

interface ValuesStepProps {
  valuesYaml: string
  setValuesYaml: (yaml: string) => void
  yamlError: string | null
  setYamlError: (error: string | null) => void
  chartDetail: ChartDetail | ArtifactHubChartDetail | undefined
  source: ChartSource
}

function ValuesStep({ valuesYaml, setValuesYaml, yamlError, setYamlError, chartDetail, source }: ValuesStepProps) {
  const [showEditor, setShowEditor] = useState(false)

  const isLocal = source === 'local'
  const localDetail = chartDetail as ChartDetail | undefined
  const ahDetail = chartDetail as ArtifactHubChartDetail | undefined

  const defaultValues = isLocal
    ? (localDetail?.values ? yaml.stringify(localDetail.values) : '')
    : (ahDetail?.values || '')

  const hasDefaults = Boolean(defaultValues)
  const hasValues = Boolean(valuesYaml)

  // Get README/docs link
  const homeUrl = isLocal ? localDetail?.home : ahDetail?.homeUrl

  return (
    <div className="space-y-4">
      {/* Info banner */}
      <div className="flex items-start gap-3 p-4 bg-blue-500/10 border border-blue-500/30 rounded-lg">
        <CheckCircle className="w-5 h-5 text-blue-400 shrink-0 mt-0.5" />
        <div>
          <p className="text-sm font-medium text-theme-text-primary">Ready to install with defaults</p>
          <p className="text-xs text-theme-text-secondary mt-1">
            {hasValues
              ? 'Default values are shown below. Edit only the values you want to change, then proceed to install.'
              : 'Most charts work out of the box. You can skip this step or customize values below.'}
          </p>
          {homeUrl && (
            <a
              href={homeUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-xs text-blue-400 hover:text-blue-300 mt-2"
            >
              <LinkIcon className="w-3 h-3" />
              View chart documentation
            </a>
          )}
        </div>
      </div>

      {/* Collapsible editor section */}
      <div className="border border-theme-border rounded-lg overflow-hidden">
        <button
          onClick={() => setShowEditor(!showEditor)}
          className="w-full flex items-center justify-between px-4 py-3 bg-theme-elevated/50 hover:bg-theme-elevated transition-colors"
        >
          <div className="flex items-center gap-2">
            <ChevronRight className={clsx('w-4 h-4 text-theme-text-tertiary transition-transform', showEditor && 'rotate-90')} />
            <span className="text-sm font-medium text-theme-text-primary">
              {hasValues ? (showEditor ? 'Hide' : 'Show') : 'Add'} configuration values
            </span>
            <span className="text-xs text-theme-text-tertiary">(optional)</span>
            {hasValues && !showEditor && (
              <span className="text-xs text-green-400 ml-2">Default values loaded</span>
            )}
          </div>
        </button>

        {showEditor && (
          <div className="p-4 border-t border-theme-border">
            {/* Action buttons */}
            <div className="flex items-center gap-3 mb-4">
              {hasDefaults && !hasValues && (
                <button
                  onClick={() => setValuesYaml(defaultValues)}
                  className="text-xs px-3 py-1.5 bg-blue-500/20 border border-blue-500/30 rounded hover:bg-blue-500/30 transition-colors text-blue-400"
                >
                  Load default values
                </button>
              )}
              {hasValues && hasDefaults && (
                <button
                  onClick={() => setValuesYaml(defaultValues)}
                  className="text-xs px-3 py-1.5 bg-theme-elevated border border-theme-border-light rounded hover:bg-theme-hover transition-colors text-theme-text-secondary"
                >
                  Reset to defaults
                </button>
              )}
              {hasValues && (
                <button
                  onClick={() => setValuesYaml('')}
                  className="text-xs text-theme-text-tertiary hover:text-theme-text-secondary"
                >
                  Clear all
                </button>
              )}
            </div>

            {yamlError && (
              <div className="flex items-center gap-2 px-3 py-2 mb-4 text-xs text-red-400 bg-red-500/10 border border-red-500/30 rounded">
                <AlertTriangle className="w-4 h-4 shrink-0" />
                {yamlError}
              </div>
            )}

            {hasValues ? (
              <YamlEditor
                value={valuesYaml}
                onChange={setValuesYaml}
                height="300px"
                onValidate={(isValid, errors) => {
                  setYamlError(isValid ? null : errors[0] || 'Invalid YAML')
                }}
              />
            ) : (
              <div className="flex flex-col items-center justify-center py-8 text-theme-text-tertiary bg-theme-base/30 rounded-lg">
                <p className="text-sm">No default values available for this chart</p>
                <p className="text-xs mt-1 text-theme-text-disabled">
                  {homeUrl ? 'Check the documentation for available options' : 'You can add custom values manually'}
                </p>
                <button
                  onClick={() => setValuesYaml('# Custom values\n')}
                  className="mt-3 text-xs px-3 py-1.5 bg-theme-elevated border border-theme-border-light rounded hover:bg-theme-hover transition-colors text-theme-text-secondary"
                >
                  Add custom values
                </button>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

interface ReviewStepProps {
  releaseName: string
  namespace: string
  chartName: string
  version: string
  repo: string
  source: ChartSource
  artifactHubRepoUrl?: string
  createNamespace: boolean
  valuesYaml: string
  defaultValuesYaml: string
}

function ReviewStep({
  releaseName,
  namespace,
  chartName,
  version,
  repo,
  source,
  artifactHubRepoUrl,
  createNamespace,
  valuesYaml,
  defaultValuesYaml,
}: ReviewStepProps) {
  const hasCustomValues = valuesYaml.trim() !== ''
  const hasDefaults = defaultValuesYaml.trim() !== ''

  // Compute the diff between defaults and current values
  const diffLines = useMemo(() => {
    if (!hasCustomValues) return null

    // If no defaults, show all custom values as additions
    if (!hasDefaults) {
      return valuesYaml.split('\n').map((line, i) => ({
        type: 'add' as const,
        content: line,
        lineNumber: i + 1,
      }))
    }

    // If values are the same as defaults, no diff
    if (valuesYaml.trim() === defaultValuesYaml.trim()) return null

    // Generate unified diff
    const patch = createPatch('values.yaml', defaultValuesYaml, valuesYaml, '', '', { context: 2 })
    const lines = patch.split('\n')

    // Parse the diff, skipping the header lines
    const result: { type: 'context' | 'add' | 'remove' | 'header'; content: string; lineNumber?: number }[] = []
    let newLineNum = 0
    let seenHeader = false

    for (const line of lines) {
      // Skip diff metadata lines
      if (line.startsWith('Index:') || line.startsWith('===') ||
          line.startsWith('---') || line.startsWith('+++')) {
        continue
      }

      // Hunk header
      if (line.startsWith('@@')) {
        seenHeader = true
        // Parse line numbers from @@ -1,3 +1,4 @@
        const match = line.match(/@@ -\d+,?\d* \+(\d+),?\d* @@/)
        if (match) {
          newLineNum = parseInt(match[1], 10)
        }
        result.push({ type: 'header', content: line })
        continue
      }

      if (!seenHeader) continue

      if (line.startsWith('+')) {
        result.push({ type: 'add', content: line.slice(1), lineNumber: newLineNum })
        newLineNum++
      } else if (line.startsWith('-')) {
        result.push({ type: 'remove', content: line.slice(1) })
      } else if (line.startsWith(' ')) {
        result.push({ type: 'context', content: line.slice(1), lineNumber: newLineNum })
        newLineNum++
      }
    }

    return result.length > 0 ? result : null
  }, [valuesYaml, defaultValuesYaml, hasCustomValues, hasDefaults])

  const hasChanges = diffLines && diffLines.some(l => l.type === 'add' || l.type === 'remove')

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3 p-4 bg-blue-500/10 border border-blue-500/30 rounded-lg">
        <CheckCircle className="w-6 h-6 text-blue-400 shrink-0" />
        <div>
          <p className="text-sm font-medium text-theme-text-primary">Ready to install</p>
          <p className="text-xs text-theme-text-secondary mt-0.5">
            Review the configuration below and click Install to proceed
          </p>
        </div>
      </div>

      {/* ArtifactHub notice */}
      {source === 'artifacthub' && (
        <div className="flex items-start gap-3 p-4 bg-amber-500/10 border border-amber-500/30 rounded-lg">
          <Globe className="w-5 h-5 text-amber-400 shrink-0 mt-0.5" />
          <div>
            <p className="text-sm font-medium text-amber-400">Installing from ArtifactHub</p>
            <p className="text-xs text-theme-text-secondary mt-1">
              This chart will be installed from: <code className="bg-theme-elevated px-1 rounded">{artifactHubRepoUrl || repo}</code>
            </p>
          </div>
        </div>
      )}

      <div className="bg-theme-elevated/30 rounded-lg p-4 space-y-3">
        <h3 className="text-sm font-medium text-theme-text-secondary">Installation Summary</h3>
        <div className="grid grid-cols-2 gap-3 text-sm">
          <div>
            <dt className="text-theme-text-tertiary">Release Name</dt>
            <dd className="text-theme-text-primary font-medium">{releaseName}</dd>
          </div>
          <div>
            <dt className="text-theme-text-tertiary">Namespace</dt>
            <dd className="text-theme-text-primary font-medium">
              {namespace}
              {createNamespace && (
                <span className="ml-2 text-xs text-amber-400">(will be created)</span>
              )}
            </dd>
          </div>
          <div>
            <dt className="text-theme-text-tertiary">Chart</dt>
            <dd className="text-theme-text-primary">{chartName}</dd>
          </div>
          <div>
            <dt className="text-theme-text-tertiary">Version</dt>
            <dd className="text-theme-text-primary">{version}</dd>
          </div>
          <div>
            <dt className="text-theme-text-tertiary">Source</dt>
            <dd className="text-theme-text-primary flex items-center gap-1">
              {source === 'local' ? (
                <>Local: {repo}</>
              ) : (
                <>
                  <Globe className="w-3.5 h-3.5 text-blue-400" />
                  ArtifactHub: {repo}
                </>
              )}
            </dd>
          </div>
          <div>
            <dt className="text-theme-text-tertiary">Custom Values</dt>
            <dd className="text-theme-text-primary">
              {hasChanges ? 'Modified' : hasCustomValues ? 'Using defaults' : 'No (using defaults)'}
            </dd>
          </div>
        </div>
      </div>

      {/* Values diff view */}
      {hasChanges && diffLines && (
        <div className="bg-theme-elevated/30 rounded-lg p-4">
          <h3 className="text-sm font-medium text-theme-text-secondary mb-3">Values Changes</h3>
          <div className="bg-theme-base/50 rounded overflow-auto max-h-64 font-mono text-xs">
            {diffLines.map((line, i) => {
              if (line.type === 'header') {
                return (
                  <div key={i} className="px-3 py-1 bg-blue-500/10 text-blue-400 border-y border-theme-border">
                    {line.content}
                  </div>
                )
              }

              return (
                <div
                  key={i}
                  className={clsx(
                    'flex items-start px-3 py-0.5',
                    line.type === 'add' && 'bg-green-500/10',
                    line.type === 'remove' && 'bg-red-500/10'
                  )}
                >
                  <span className={clsx(
                    'w-4 shrink-0',
                    line.type === 'add' && 'text-green-400',
                    line.type === 'remove' && 'text-red-400',
                    line.type === 'context' && 'text-theme-text-disabled'
                  )}>
                    {line.type === 'add' && <Plus className="w-3 h-3 inline" />}
                    {line.type === 'remove' && <Minus className="w-3 h-3 inline" />}
                  </span>
                  <span className={clsx(
                    'flex-1 whitespace-pre',
                    line.type === 'add' && 'text-green-400',
                    line.type === 'remove' && 'text-red-400',
                    line.type === 'context' && 'text-theme-text-secondary'
                  )}>
                    {line.content || ' '}
                  </span>
                </div>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}

// Installing step - shows progress logs during installation
interface InstallingStepProps {
  releaseName: string
  namespace: string
  chartName: string
  progressLogs: ProgressEntry[]
  isInstalling: boolean
  installError: string | null
  progressEndRef: React.RefObject<HTMLDivElement>
}

function InstallingStep({
  releaseName,
  namespace,
  chartName,
  progressLogs,
  isInstalling,
  installError,
  progressEndRef,
}: InstallingStepProps) {
  const isComplete = progressLogs.some(l => l.phase === 'complete')

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className={clsx(
        'flex items-center gap-3 p-4 rounded-lg border',
        isComplete ? 'bg-green-500/10 border-green-500/30' :
        installError ? 'bg-red-500/10 border-red-500/30' :
        'bg-blue-500/10 border-blue-500/30'
      )}>
        {isComplete ? (
          <CheckCircle className="w-6 h-6 text-green-400 shrink-0" />
        ) : installError ? (
          <AlertTriangle className="w-6 h-6 text-red-400 shrink-0" />
        ) : (
          <Loader2 className="w-6 h-6 text-blue-400 shrink-0 animate-spin" />
        )}
        <div>
          <p className={clsx(
            'text-sm font-medium',
            isComplete ? 'text-green-400' :
            installError ? 'text-red-400' :
            'text-theme-text-primary'
          )}>
            {isComplete ? 'Installation Complete' :
             installError ? 'Installation Failed' :
             `Installing ${chartName}...`}
          </p>
          <p className="text-xs text-theme-text-secondary mt-0.5">
            {isComplete ? `${releaseName} is now running in ${namespace}` :
             installError ? 'See logs below for details' :
             `Deploying to namespace ${namespace}`}
          </p>
        </div>
      </div>

      {/* Progress logs */}
      <div className="bg-theme-base rounded-lg border border-theme-border overflow-hidden">
        <div className="flex items-center gap-2 px-4 py-2 border-b border-theme-border bg-theme-elevated/50">
          <Terminal className="w-4 h-4 text-theme-text-tertiary" />
          <span className="text-xs font-medium text-theme-text-secondary">Install Progress</span>
        </div>
        <div className="p-3 max-h-64 overflow-auto font-mono text-xs">
          {progressLogs.length === 0 && isInstalling && (
            <div className="text-theme-text-tertiary">Starting installation...</div>
          )}
          {progressLogs.map((log, i) => (
            <div
              key={i}
              className={clsx(
                'flex items-start gap-2 py-1',
                log.phase === 'error' && 'text-red-400',
                log.phase === 'complete' && 'text-green-400'
              )}
            >
              <span className="text-theme-text-disabled shrink-0 w-16">
                {log.timestamp.toLocaleTimeString()}
              </span>
              <span className={clsx(
                'shrink-0 px-1.5 py-0.5 rounded text-xs uppercase',
                log.phase === 'error' ? 'bg-red-500/20 text-red-400' :
                log.phase === 'complete' ? 'bg-green-500/20 text-green-400' :
                'bg-theme-elevated text-theme-text-tertiary'
              )}>
                {log.phase}
              </span>
              <span className={clsx(
                log.phase === 'error' ? 'text-red-400' :
                log.phase === 'complete' ? 'text-green-400' :
                'text-theme-text-secondary'
              )}>
                {log.message}
              </span>
            </div>
          ))}
          {isInstalling && (
            <div className="flex items-center gap-2 py-1 text-theme-text-tertiary">
              <Loader2 className="w-3 h-3 animate-spin" />
              <span>Waiting for resources...</span>
            </div>
          )}
          <div ref={progressEndRef} />
        </div>
      </div>

      {/* Error details if failed */}
      {installError && (
        <div className="bg-red-500/10 border border-red-500/30 rounded-lg p-4">
          <p className="text-sm font-medium text-red-400 mb-2">Error Details</p>
          <pre className="text-xs text-red-300 whitespace-pre-wrap font-mono">
            {installError}
          </pre>
        </div>
      )}
    </div>
  )
}
