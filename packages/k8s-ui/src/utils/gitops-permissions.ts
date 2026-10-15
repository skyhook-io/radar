/** One API operation a GitOps action performs; `source` marks a Flux source reconciled alongside the target. */
export interface GitOpsPermission {
  verb: string
  group?: string
  resource: string
  namespace: string
  name?: string
  kind?: string
  source?: boolean
}

const VERB_ORDER = ['get', 'patch']

function verbsText(denials: GitOpsPermission[]): string {
  const verbs = [...new Set(denials.map(denial => denial.verb))]
  const rank = (verb: string) => VERB_ORDER.includes(verb) ? VERB_ORDER.indexOf(verb) : VERB_ORDER.length
  return verbs.sort((a, b) => rank(a) - rank(b)).map(verb => verb === 'get' ? 'read' : verb).join(' or ')
}

/** One sentence naming each object the role can't act on. */
export function gitOpsDenialMessage(denials: GitOpsPermission[]): string {
  const target = denials.filter(denial => !denial.source)
  const source = denials.filter(denial => denial.source)
  const sources = new Map<string, GitOpsPermission[]>()
  for (const denial of source) {
    const key = [denial.group, denial.resource, denial.namespace, denial.name].join('\u0000')
    const operations = sources.get(key) ?? []
    operations.push(denial)
    sources.set(key, operations)
  }
  if (sources.size > 1) {
    const clauses = [...sources.values()].map(operations => {
      const object = operations[0]
      return `${verbsText(operations)} source ${object.kind || object.resource} ${object.namespace}/${object.name}`
    })
    if (target.length > 0) {
      const object = target[0]
      clauses.unshift(`${verbsText(target)} Flux ${object.kind || object.resource}${object.name ? ` ${object.name}` : ''} in ${object.namespace}`)
    }
    return `Your role can't ${clauses.join(' or ')}.`
  }
  const sourceLabel = source[0] && `${source[0].kind || source[0].resource} ${source[0].namespace}/${source[0].name}`
  if (target.length === 0) {
    return sourceLabel
      ? `Sync with source also reconciles ${sourceLabel} — your role can't ${verbsText(source)} it.`
      : "Your role can't perform this action."
  }
  const object = target[0]
  const tool = object.group === 'argoproj.io' ? 'Argo CD' : 'Flux'
  let message = `Your role can't ${verbsText(target)} ${tool} ${object.kind || object.resource}${object.name ? ` ${object.name}` : ''} in ${object.namespace}`
  if (sourceLabel) {
    message += verbsText(source) === verbsText(target)
      ? ` or its source ${sourceLabel}`
      : `, or ${verbsText(source)} its source ${sourceLabel}`
  }
  return `${message}.`
}

/** Distinct denied operations across actions, in first-seen order. */
export function uniqueGitOpsPermissions(denials: GitOpsPermission[]): GitOpsPermission[] {
  const seen = new Set<string>()
  return denials.filter(denial => {
    const key = [denial.verb, denial.group, denial.resource, denial.namespace, denial.name, denial.source].join('\u0000')
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}
