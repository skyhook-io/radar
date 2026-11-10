// Human-friendly summaries for common Kubernetes API error patterns.
// Falls back to null when no known pattern matches — caller shows raw error.

interface FriendlyError {
  summary: string
  suggestion: string
}

const patterns: Array<{ test: (msg: string) => boolean; friendly: (msg: string, allowApply: boolean) => FriendlyError }> = [
  // Admission webhook timeout
  {
    test: (msg) => /failed calling webhook.*context deadline exceeded/i.test(msg),
    friendly: (msg) => {
      const webhook = msg.match(/webhook "([^"]+)"/)?.[1] ?? 'unknown'
      const service = webhook.split('.')[0] || webhook
      return {
        summary: `Admission webhook timed out (${service})`,
        suggestion: 'A cluster validation webhook failed to respond. This is usually a temporary cluster issue, not a problem with your YAML. Try again or contact your cluster admin.',
      }
    },
  },
  // Admission webhook rejected
  {
    test: (msg) => /admission webhook.*denied the request/i.test(msg),
    friendly: (msg) => {
      const webhook = msg.match(/webhook "([^"]+)"/)?.[1] ?? 'unknown'
      return {
        summary: `Rejected by admission policy (${webhook.split('.')[0]})`,
        suggestion: 'A cluster admission webhook rejected this resource. Check that your YAML meets the cluster\'s policies, or contact your cluster admin.',
      }
    },
  },
  // Validation error — invalid field value
  {
    test: (msg) => /is invalid:.*Invalid value/i.test(msg),
    friendly: (msg) => {
      const fields = [...msg.matchAll(/(\S+): Invalid value/g)].map(m => m[1])
      const fieldList = fields.length > 0 ? fields.join(', ') : 'unknown field'
      return {
        summary: `Validation failed: invalid value for ${fieldList}`,
        suggestion: 'One or more field values don\'t match what Kubernetes expects. Check the highlighted fields in the error details.',
      }
    },
  },
  // Validation error — selector doesn't match labels
  {
    test: (msg) => /`selector` does not match template `labels`/i.test(msg),
    friendly: () => ({
      summary: 'Selector doesn\'t match template labels',
      suggestion: 'The spec.selector.matchLabels must match spec.template.metadata.labels exactly.',
    }),
  },
  // Validation error — required field
  {
    test: (msg) => /Required value|is required/i.test(msg),
    friendly: (msg) => {
      const field = msg.match(/(\S+): Required value/)?.[1] ?? msg.match(/(\S+) is required/)?.[1] ?? ''
      return {
        summary: `Missing required field${field ? `: ${field}` : ''}`,
        suggestion: 'Add the missing required field to your YAML.',
      }
    },
  },
  // RBAC forbidden — use "is forbidden:" to avoid matching "Forbidden: field is immutable" or "forbidden: exceeded quota"
  {
    test: (msg) => /is forbidden:|cannot .* in the namespace/i.test(msg),
    friendly: (msg) => {
      const verb = msg.match(/cannot (\w+)/)?.[1] ?? 'perform this action on'
      return {
        summary: `Permission denied`,
        suggestion: `Your cluster role doesn't allow you to ${verb} this resource. Contact your cluster admin for access.`,
      }
    },
  },
  // Already exists (create mode)
  {
    test: (msg) => /already exists/i.test(msg),
    friendly: (_msg, allowApply) => ({
      summary: 'Resource already exists',
      suggestion: allowApply ? 'Switch to Apply mode to update the existing resource, or change the name to create a new one.' : 'Change the name to create a new resource, or remove this document if it was already created.',
    }),
  },
  // Quota exceeded
  {
    test: (msg) => /exceeded quota|quota.*exceeded|forbidden.*quota/i.test(msg),
    friendly: () => ({
      summary: 'Resource quota exceeded',
      suggestion: 'The namespace has reached its resource quota limit. Request a quota increase or reduce resource requests.',
    }),
  },
  // Namespace not found
  {
    test: (msg) => /namespace.*not found|namespaces.*not found/i.test(msg),
    friendly: (msg) => {
      const ns = msg.match(/namespace[s]? "([^"]+)"/)?.[1] ?? ''
      return {
        summary: `Namespace "${ns}" not found`,
        suggestion: 'Create the namespace first or change the namespace in your YAML.',
      }
    },
  },
  // Immutable field
  {
    test: (msg) => /field is immutable/i.test(msg),
    friendly: () => ({
      summary: 'Cannot change immutable field',
      suggestion: 'Some fields can\'t be modified after creation. Delete and recreate the resource, or remove the immutable field change.',
    }),
  },
  // Connection refused / cluster unreachable
  {
    test: (msg) => /connection refused|no such host|cluster.*unreachable/i.test(msg),
    friendly: () => ({
      summary: 'Cannot reach the cluster',
      suggestion: 'The Kubernetes API server is unreachable. Check your cluster connection.',
    }),
  },
]

// Strip the "failed to apply resource: " / "failed to create resource: " prefix our backend adds
function stripPrefix(msg: string): string {
  return msg.replace(/^failed to (apply|create) resource:\s*/i, '')
}

export function getFriendlyError(rawError: string, allowApply = true): { summary: string; suggestion: string; raw: string } | null {
  const cleaned = stripPrefix(rawError)
  for (const pattern of patterns) {
    if (pattern.test(cleaned)) {
      const { summary, suggestion } = pattern.friendly(cleaned, allowApply)
      return { summary, suggestion, raw: cleaned }
    }
  }
  return null
}

// Always returns something displayable — friendly if matched, raw if not
export function formatApplyError(rawError: string, { allowApply = true }: { allowApply?: boolean } = {}): { summary: string; suggestion?: string; raw: string } {
  const friendly = getFriendlyError(rawError, allowApply)
  if (friendly) return friendly
  const cleaned = stripPrefix(rawError)
  return { summary: cleaned, raw: rawError }
}
