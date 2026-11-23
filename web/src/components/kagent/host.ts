import type { ResourceHost } from '../../integrations/resourceHost'
import { KagentAgentRenderer } from './KagentAgentRenderer'

/** kagent's Agent drawer reads the AgentTemplate and Harness the Agent references. */
export const kagentResourceHost: ResourceHost = {
  id: 'kagent',
  renderers: { KagentAgentRenderer },
}
