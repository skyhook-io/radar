import { ArrowLeft, ArrowUpRight } from 'lucide-react'
import { SELF_HOSTED_DOCS_URL, signupUrlFor } from './cloudConnectHandoff'

// What the Cloud dialog shows after "Run Radar Cloud yourself…". Running your
// own control plane starts with a Radar Cloud account, because the Hub signs
// the Self-Managed license, so this view names that as step 1 of 3 before the
// click instead of letting a sign-up page surprise someone who came to host
// it themselves. The link carries intent=self-hosting: the Hub's sign-in page
// repeats the steps and, after sign-up or sign-in, continues on its
// Self-hosting page, where the trial, the license and the install command are.
export function SelfManagedStart({ appUrl, onBack }: { appUrl: string; onBack: () => void }) {
  const signupUrl = `${signupUrlFor(appUrl, 'self-managed-signup')}&intent=self-hosting`
  const steps = [
    {
      title: 'Create your Radar Cloud account',
      body: 'Your license is issued from it. An existing account works too.',
    },
    {
      title: 'Generate your install command',
      body: 'Self-Managed is part of Enterprise, with a 14-day free trial.',
    },
    {
      title: 'Install the control plane, then connect each cluster',
      body: 'One Helm command installs it. Then one command per cluster to connect it.',
    },
  ]
  return (
    <>
      <div className="min-h-0 overflow-y-auto">
        <div className="px-8 pb-5">
          <h3 className="text-[20px] font-semibold leading-tight tracking-tight text-theme-text-primary mb-1.5">
            Run Radar Cloud yourself
          </h3>
          <p className="text-[13px] leading-relaxed text-theme-text-secondary mb-5">
            Radar Cloud Self-Managed runs the control plane and dashboard in your own infrastructure: the same fleet
            view, alerts, history and team access, with cluster data staying inside your network.
          </p>
          <ol className="space-y-3.5">
            {steps.map((step, i) => (
              <li key={step.title} className="flex gap-3">
                <span
                  className={`flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-[10.5px] font-semibold ${
                    i === 0 ? 'bg-emerald-500 text-emerald-950' : 'border border-theme-border text-theme-text-tertiary'
                  }`}
                >
                  {i + 1}
                </span>
                <div className="min-w-0">
                  <p className={`text-[13px] font-medium ${i === 0 ? 'text-theme-text-primary' : 'text-theme-text-secondary'}`}>
                    {step.title}
                  </p>
                  <p className="text-[12px] leading-relaxed text-theme-text-tertiary">{step.body}</p>
                </div>
              </li>
            ))}
          </ol>
          {/* What a platform engineer checks before starting: where it is
              reached, what it stores in, and whether an IdP is needed on day
              one (it isn't: the break-glass admin signs in first). */}
          <div className="mt-5 text-[11.5px] leading-relaxed text-theme-text-tertiary">
            <p className="font-medium text-theme-text-secondary">You'll need:</p>
            <ul className="mt-1 space-y-0.5 list-disc pl-4">
              <li>A hostname and an ingress for the control plane</li>
              <li>Postgres 14+ for production. A bundled one runs by default, fine for a trial</li>
              <li>Nothing for sign-in at first: a built-in admin works until you connect your IdP</li>
            </ul>
            <a href={SELF_HOSTED_DOCS_URL} target="_blank" rel="noopener noreferrer" className="mt-1.5 inline-block underline underline-offset-2 hover:text-theme-text-primary">
              Read the Self-Managed guide →
            </a>
          </div>
        </div>
      </div>
      <div className="shrink-0 px-8 py-4 bg-theme-base border-t border-theme-border flex flex-wrap items-center gap-x-3 gap-y-2.5">
        <button
          onClick={onBack}
          className="inline-flex items-center gap-1.5 whitespace-nowrap text-[12.5px] text-theme-text-secondary hover:text-theme-text-primary transition-colors"
        >
          <ArrowLeft className="w-3.5 h-3.5" />
          Back to Radar Cloud
        </button>
        <a
          href={signupUrl}
          target="_blank"
          rel="noopener noreferrer"
          className="ml-auto inline-flex items-center gap-1.5 whitespace-nowrap px-5 py-2 rounded-[10px] bg-emerald-500 hover:bg-emerald-400 text-emerald-950 text-[13.5px] font-bold shadow-[0_0_22px_rgba(16,185,129,0.35)] hover:shadow-[0_0_30px_rgba(16,185,129,0.5)] transition-all"
        >
          Create your Radar Cloud account
          <ArrowUpRight className="w-4 h-4" />
        </a>
      </div>
    </>
  )
}
