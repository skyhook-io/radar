import { useState, useEffect } from 'react'
import type { TrafficSourcesResponse, TrafficWizardState } from '../../types'
import { Loader2, CheckCircle2, XCircle, Copy, ExternalLink, ArrowRight, ArrowLeft, Package } from 'lucide-react'
import { InstallWizard } from '../helm/InstallWizard'

interface TrafficWizardProps {
  state: TrafficWizardState
  setState: (state: TrafficWizardState) => void
  sourcesData?: TrafficSourcesResponse
  sourcesLoading: boolean
  onRefetch: () => void
}

export function TrafficWizard({
  state,
  setState,
  sourcesData,
  sourcesLoading,
  onRefetch,
}: TrafficWizardProps) {
  const [step, setStep] = useState<'choice' | 'install'>('choice')
  const [copied, setCopied] = useState(false)
  const [lastChecked, setLastChecked] = useState<Date | null>(null)
  const [showHelmInstall, setShowHelmInstall] = useState(false)

  const cluster = sourcesData?.cluster
  const recommendation = sourcesData?.recommended
  const helmChart = recommendation?.helmChart

  // Auto-check when in 'checking' state
  useEffect(() => {
    if (state !== 'checking') return

    // Initial check
    onRefetch()
    setLastChecked(new Date())

    const interval = setInterval(() => {
      onRefetch()
      setLastChecked(new Date())
    }, 30000) // Check every 30 seconds

    return () => clearInterval(interval)
  }, [state, onRefetch])

  // Format relative time
  const formatLastChecked = (date: Date | null): string => {
    if (!date) return 'never'
    const seconds = Math.floor((Date.now() - date.getTime()) / 1000)
    if (seconds < 5) return 'just now'
    if (seconds < 60) return `${seconds}s ago`
    const minutes = Math.floor(seconds / 60)
    return `${minutes}m ${seconds % 60}s ago`
  }

  // Update display every second when checking
  const [, setTick] = useState(0)
  useEffect(() => {
    if (state !== 'checking') return
    const interval = setInterval(() => setTick(t => t + 1), 1000)
    return () => clearInterval(interval)
  }, [state])

  // Copy command to clipboard
  const copyCommand = (command: string) => {
    navigator.clipboard.writeText(command)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  // Render Helm InstallWizard as overlay if shown
  const helmInstallOverlay = showHelmInstall && helmChart ? (
    <InstallWizard
      repo={helmChart.repo}
      chartName={helmChart.chartName}
      version={helmChart.version || 'latest'}
      source="artifacthub"
      repoUrl={helmChart.repoUrl}  // Pass direct repo URL for non-ArtifactHub charts
      onClose={() => setShowHelmInstall(false)}
      onSuccess={() => {
        setShowHelmInstall(false)
        setState('checking')
        setLastChecked(null)
      }}
    />
  ) : null

  // Detecting state
  if (state === 'detecting' || sourcesLoading) {
    return (
      <div className="flex items-center justify-center h-full w-full">
        <div className="text-center space-y-4">
          <Loader2 className="h-8 w-8 animate-spin text-blue-400 mx-auto" />
          <p className="text-theme-text-secondary">Detecting traffic sources...</p>
        </div>
      </div>
    )
  }

  // Checking state (polling for newly enabled source)
  if (state === 'checking') {
    return (
      <>
        <div className="flex items-center justify-center h-full w-full">
          <div className="max-w-md w-full p-6 space-y-6">
            <div className="text-center space-y-2">
              <Loader2 className="h-8 w-8 animate-spin text-blue-400 mx-auto" />
              <h2 className="text-lg font-medium text-theme-text-primary">Waiting for traffic source...</h2>
              <p className="text-sm text-theme-text-secondary">
                Checking for availability
              </p>
            </div>

            <div className="bg-theme-elevated/50 rounded-lg p-4 text-sm">
              <p className="text-theme-text-tertiary">
                Last checked: {formatLastChecked(lastChecked)}
              </p>
              <p className="text-theme-text-tertiary">
                Polls every 30 seconds automatically
              </p>
            </div>

            <div className="flex gap-2 justify-center">
              <button
                onClick={() => {
                  setStep('choice')
                  setState('wizard')
                }}
                className="flex items-center gap-1 px-3 py-2 text-sm rounded border border-theme-border text-theme-text-primary hover:bg-theme-hover transition-colors"
              >
                <ArrowLeft className="h-4 w-4" />
                Back
              </button>
              <button
                onClick={() => {
                  onRefetch()
                  setLastChecked(new Date())
                }}
                className="px-3 py-2 text-sm rounded border border-theme-border text-theme-text-primary hover:bg-theme-hover transition-colors"
              >
                Check Now
              </button>
            </div>
          </div>
        </div>
        {helmInstallOverlay}
      </>
    )
  }

  // Step 1: Choice screen
  if (step === 'choice') {
    return (
      <>
        <div className="flex items-center justify-center h-full w-full">
          <div className="max-w-lg w-full p-6 space-y-6">
            <div className="space-y-2">
              <h2 className="text-lg font-medium text-theme-text-primary">Traffic Visibility</h2>
              <p className="text-sm text-theme-text-secondary">
                View service-to-service network flows in your cluster
              </p>
            </div>

            {/* Cluster detection results */}
            <div className="bg-theme-elevated/50 rounded-lg p-4 space-y-3">
              <h3 className="text-sm font-medium text-theme-text-primary">Cluster Detection</h3>
              <div className="grid grid-cols-2 gap-2 text-sm">
                <div className="flex items-center gap-2 text-theme-text-secondary">
                  <CheckCircle2 className="h-4 w-4 text-green-500" />
                  <span>Platform: {cluster?.platform || 'generic'}</span>
                </div>
                <div className="flex items-center gap-2 text-theme-text-secondary">
                  {cluster?.cni && cluster.cni !== 'unknown' ? (
                    <CheckCircle2 className="h-4 w-4 text-green-500" />
                  ) : (
                    <XCircle className="h-4 w-4 text-yellow-500" />
                  )}
                  <span>CNI: {cluster?.cni || 'unknown'}</span>
                </div>
                {cluster?.cni === 'cilium' && (
                  <div className="flex items-center gap-2 col-span-2 text-theme-text-secondary">
                    {cluster.dataplaneV2 ? (
                      <CheckCircle2 className="h-4 w-4 text-green-500" />
                    ) : (
                      <XCircle className="h-4 w-4 text-yellow-500" />
                    )}
                    <span>
                      Hubble: {cluster.dataplaneV2 ? 'Enabled' : 'Not enabled'}
                    </span>
                  </div>
                )}
              </div>
            </div>

            {/* Recommendation */}
            {recommendation && (
              <div className="space-y-4">
                {/* Primary option */}
                <div className="border border-blue-500/30 bg-blue-500/5 rounded-lg p-4 space-y-3">
                  <div className="flex items-start gap-2">
                    <span className="px-2 py-0.5 text-xs font-medium bg-blue-500 text-white rounded">
                      Recommended
                    </span>
                    <span className="font-medium capitalize text-theme-text-primary">{recommendation.name}</span>
                  </div>
                  <p className="text-sm text-theme-text-secondary">{recommendation.reason}</p>

                  {/* Helm install button if chart info available */}
                  {recommendation.helmChart ? (
                    <button
                      onClick={() => setShowHelmInstall(true)}
                      className="w-full flex items-center justify-center gap-2 px-4 py-2 text-sm font-medium rounded bg-blue-500 text-white hover:bg-blue-600 transition-colors"
                    >
                      <Package className="h-4 w-4" />
                      Install {recommendation.name} with Helm
                    </button>
                  ) : (
                    <button
                      onClick={() => setStep('install')}
                      className="w-full flex items-center justify-center gap-1 px-4 py-2 text-sm font-medium rounded bg-blue-500 text-white hover:bg-blue-600 transition-colors"
                    >
                      View install instructions
                      <ArrowRight className="h-4 w-4" />
                    </button>
                  )}

                  {/* Documentation link */}
                  {recommendation.docsUrl && (
                    <a
                      href={recommendation.docsUrl}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="inline-flex items-center gap-1 text-sm text-blue-400 hover:underline"
                    >
                      View documentation
                      <ExternalLink className="h-4 w-4" />
                    </a>
                  )}
                </div>

                {/* Alternative option (if available) */}
                {recommendation.alternativeName && (
                  <div className="border border-theme-border rounded-lg p-4 space-y-3">
                    <div className="flex items-start gap-2">
                      <span className="px-2 py-0.5 text-xs font-medium bg-theme-elevated text-theme-text-secondary rounded">
                        Alternative
                      </span>
                      <span className="font-medium capitalize text-theme-text-primary">{recommendation.alternativeName}</span>
                    </div>
                    <p className="text-sm text-theme-text-secondary">{recommendation.alternativeReason}</p>
                    {recommendation.alternativeDocsUrl && (
                      <a
                        href={recommendation.alternativeDocsUrl}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="inline-flex items-center gap-1 text-sm text-blue-400 hover:underline"
                      >
                        Learn more about {recommendation.alternativeName}
                        <ExternalLink className="h-4 w-4" />
                      </a>
                    )}
                  </div>
                )}
              </div>
            )}

            {/* No recommendation */}
            {!recommendation && (
              <div className="border border-theme-border rounded-lg p-4 space-y-3">
                <p className="text-sm text-theme-text-secondary">
                  No traffic source detected. Install Cilium with Hubble for traffic visibility.
                </p>
                <a
                  href="https://docs.cilium.io/en/stable/gettingstarted/hubble/"
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-1 px-3 py-2 text-sm rounded border border-theme-border text-theme-text-primary hover:bg-theme-hover transition-colors"
                >
                  View Hubble Documentation
                  <ExternalLink className="h-4 w-4" />
                </a>
              </div>
            )}
          </div>
        </div>
        {helmInstallOverlay}
      </>
    )
  }

  // Step 2: Install instructions (for non-Helm installs like gcloud commands)
  return (
    <>
      <div className="flex items-center justify-center h-full w-full">
        <div className="max-w-lg w-full p-6 space-y-6">
          <div className="space-y-2">
            <h2 className="text-lg font-medium text-theme-text-primary">Enable {recommendation?.name || 'Hubble'}</h2>
            <p className="text-sm text-theme-text-secondary">
              Run the following command to enable traffic observability
            </p>
          </div>

          {/* Install command */}
          <div className="bg-theme-elevated rounded-lg p-4 space-y-3">
            <div className="flex items-center justify-between">
              <span className="text-xs font-medium text-theme-text-tertiary">Command</span>
              <button
                onClick={() => copyCommand(recommendation?.installCommand || '')}
                className="p-1 rounded text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover transition-colors"
              >
                {copied ? (
                  <CheckCircle2 className="h-4 w-4 text-green-500" />
                ) : (
                  <Copy className="h-4 w-4" />
                )}
              </button>
            </div>
            <pre className="text-sm font-mono whitespace-pre-wrap break-all bg-theme-surface rounded p-3 overflow-x-auto text-theme-text-primary">
              {recommendation?.installCommand || 'No command available'}
            </pre>
          </div>

          {/* Platform-specific notes */}
          {cluster?.platform === 'gke' && cluster?.cni === 'cilium' && (
            <div className="bg-blue-500/10 border border-blue-500/20 rounded-lg p-4 text-sm">
              <p className="text-blue-400 font-medium">GKE Dataplane V2</p>
              <p className="text-theme-text-secondary mt-1">
                Your cluster uses Cilium natively. The command above enables the Hubble
                observability layer which provides traffic visibility without any performance impact.
              </p>
            </div>
          )}

          {/* Documentation link */}
          {recommendation?.docsUrl && (
            <a
              href={recommendation.docsUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="flex items-center gap-2 text-sm text-blue-400 hover:underline"
            >
              View full documentation
              <ExternalLink className="h-4 w-4" />
            </a>
          )}

          {/* Action buttons */}
          <div className="flex gap-2">
            <button
              onClick={() => setStep('choice')}
              className="flex items-center gap-1 px-3 py-2 text-sm rounded border border-theme-border text-theme-text-primary hover:bg-theme-hover transition-colors"
            >
              <ArrowLeft className="h-4 w-4" />
              Back
            </button>
            <button
              onClick={() => {
                setState('checking')
                setLastChecked(null)
              }}
              className="flex-1 px-4 py-2 text-sm font-medium rounded bg-blue-500 text-white hover:bg-blue-600 transition-colors"
            >
              I've run the command
            </button>
          </div>
        </div>
      </div>
      {helmInstallOverlay}
    </>
  )
}
