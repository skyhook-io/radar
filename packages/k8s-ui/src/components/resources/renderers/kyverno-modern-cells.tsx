// Cell components for the modern Kyverno CEL family, PolicyException and
// CleanupPolicy rows in ResourcesView.

import { clsx } from 'clsx'
import { formatAge } from '../resource-utils'
import {
  getKyvernoAttestors,
  getKyvernoDeletionConditions,
  getKyvernoGenerations,
  getKyvernoImageReferences,
  getKyvernoMatchSummary,
  getKyvernoMutations,
  getKyvernoPolicyFamily,
  getKyvernoLastExecutionTime,
  getKyvernoSchedule,
  getKyvernoValidations,
  getModernKyvernoPolicyStatus,
} from '../resource-utils-kyverno-modern'
import {
  getKyvernoCleanupMatchedKinds,
  getKyvernoCleanupPolicyStatus,
  getKyvernoCleanupSchedule,
  getKyvernoExceptedPolicies,
  getKyvernoPolicyExceptionStatus,
  isBlanketKyvernoException,
} from '../resource-utils-kyverno-exceptions'

const dash = <span className="text-sm text-theme-text-tertiary">-</span>

/** Counts the per-family "rules" the row's Rules/Mutations/Generates column shows. */
function ruleCount(resource: any): number {
  switch (getKyvernoPolicyFamily(resource?.kind)) {
    case 'mutating':
      return getKyvernoMutations(resource).length
    case 'generating':
      return getKyvernoGenerations(resource).length
    case 'deleting':
      return getKyvernoDeletionConditions(resource).length
    default:
      return getKyvernoValidations(resource).length
  }
}

export function KyvernoModernPolicyCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getModernKyvernoPolicyStatus(resource)
      return <span className={clsx('badge', status.color)}>{status.text}</span>
    }
    case 'appliesTo':
      return <span className="text-sm text-theme-text-secondary">{getKyvernoMatchSummary(resource)}</span>
    case 'rules': {
      const count = ruleCount(resource)
      // A DeletingPolicy with zero conditions deletes everything it matches,
      // so a bare "0" would read as harmless when it is the opposite.
      if (getKyvernoPolicyFamily(resource?.kind) === 'deleting' && count === 0) {
        return <span className="text-sm text-theme-text-secondary">All matched</span>
      }
      return <span className="text-sm text-theme-text-secondary">{count}</span>
    }
    case 'lastRun': {
      // Absence is stated, never blank. A blank cell reads the same whether the
      // policy ran and we failed to show it or it has never run at all — and
      // "never run" is the actual failure mode for a scheduled policy, so it
      // has to be the loud state rather than the empty one.
      const last = getKyvernoLastExecutionTime(resource)
      if (!last) {
        return (
          <span className="badge status-degraded" title="This policy has not run since it was created">
            Never run
          </span>
        )
      }
      return <span className="text-sm text-theme-text-secondary">{formatAge(last)} ago</span>
    }
    case 'schedule': {
      const schedule = getKyvernoSchedule(resource)
      return schedule ? (
        <span className="text-sm font-mono text-theme-text-secondary">{schedule}</span>
      ) : (
        <span className="text-sm text-theme-text-tertiary">Not set</span>
      )
    }
    case 'images': {
      const refs = getKyvernoImageReferences(resource)
      if (refs.length === 0) return <span className="text-sm text-theme-text-tertiary">All</span>
      return (
        <span className="text-sm font-mono text-theme-text-secondary truncate">
          {refs.length === 1 ? refs[0] : `${refs[0]} +${refs.length - 1}`}
        </span>
      )
    }
    case 'attestors': {
      const attestors = getKyvernoAttestors(resource)
      // No attestor means nothing to verify signatures against.
      if (attestors.length === 0) return <span className="badge status-degraded">None</span>
      return <span className="text-sm text-theme-text-secondary">{attestors.length}</span>
    }
    default:
      return dash
  }
}

export function KyvernoPolicyExceptionCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getKyvernoPolicyExceptionStatus(resource)
      return <span className={clsx('badge', status.color)}>{status.text}</span>
    }
    case 'policies': {
      if (isBlanketKyvernoException(resource)) {
        return <span className="text-sm text-theme-text-secondary">Every matching policy</span>
      }
      const names = getKyvernoExceptedPolicies(resource).map((p) => p.name)
      return (
        <span className="text-sm text-theme-text-secondary truncate">
          {names.length <= 2 ? names.join(', ') : `${names.slice(0, 2).join(', ')} +${names.length - 2}`}
        </span>
      )
    }
    default:
      return dash
  }
}

export function KyvernoCleanupPolicyCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getKyvernoCleanupPolicyStatus(resource)
      return <span className={clsx('badge', status.color)}>{status.text}</span>
    }
    case 'schedule': {
      const schedule = getKyvernoCleanupSchedule(resource)
      return schedule ? (
        <span className="text-sm font-mono text-theme-text-secondary">{schedule}</span>
      ) : (
        <span className="text-sm text-theme-text-tertiary">Not set</span>
      )
    }
    case 'appliesTo': {
      const kinds = getKyvernoCleanupMatchedKinds(resource)
      if (kinds.length === 0) return dash
      return (
        <span className="text-sm text-theme-text-secondary truncate">
          {kinds.length <= 3 ? kinds.join(', ') : `${kinds.slice(0, 3).join(', ')} +${kinds.length - 3}`}
        </span>
      )
    }
    default:
      return dash
  }
}
