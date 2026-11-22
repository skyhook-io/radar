import { KAGENT_GROUP } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kagent'

export { KAGENT_GROUP }

export const KAGENT_AGENT_KIND = { name: 'agents', kind: 'Agent', group: KAGENT_GROUP }

export const KAGENT_AGENTS_PATH = `/resources/${KAGENT_AGENT_KIND.name}?apiGroup=${encodeURIComponent(KAGENT_GROUP)}`
