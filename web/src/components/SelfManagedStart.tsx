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
      body: 'Your Self-Managed license is issued from it, so it comes first. An existing account works too.',
    },
    {
      title: 'Unlock Enterprise and generate your install command',
      body: 'Radar Cloud takes you straight to Self-hosting. Self-Managed is part of Enterprise; a new organization can start with a 14-day trial.',
    },
    {
      title: 'Install it and connect your clusters',
      body: 'One Helm command in a cluster you choose, then connect clusters to your own control plane.',
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
          <p className="mt-5 text-[11.5px] leading-relaxed text-theme-text-tertiary">
            You&apos;ll need a Kubernetes cluster on 1.27 or newer, a hostname you control, and Postgres; the bundled
            one is fine for a trial.{' '}
            <a href={SELF_HOSTED_DOCS_URL} target="_blank" rel="noopener noreferrer" className="whitespace-nowrap underline underline-offset-2 hover:text-theme-text-primary">
              Read the Self-Managed guide
            </a>
          </p>
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
