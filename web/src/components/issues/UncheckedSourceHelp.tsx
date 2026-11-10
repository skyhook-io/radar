import { Disclosure } from '@skyhook-io/k8s-ui'

export function UncheckedSourceHelp({ onRetry, isRefreshing }: { onRetry: () => void; isRefreshing: boolean }) {
  return (
    <Disclosure
      summary="Why is this shown?"
      className="mt-2 text-left text-xs text-theme-text-secondary"
      summaryClassName="min-h-9 text-accent-text hover:underline"
    >
      <div className="space-y-2 pt-2 leading-5">
        <p>Some checks didn’t return a usable result. Issues found by other checks are still shown.</p>
        <p>Radar checks again automatically. If this keeps happening, check Radar’s connection and access to Kubernetes.</p>
        <button
          type="button"
          onClick={onRetry}
          disabled={isRefreshing}
          className="btn-secondary min-h-9 min-w-32 px-2.5 py-1 font-medium disabled:cursor-not-allowed"
        >
          {isRefreshing ? 'Checking again…' : 'Check again'}
        </button>
      </div>
    </Disclosure>
  )
}
