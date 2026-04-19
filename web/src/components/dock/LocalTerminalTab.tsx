import { LocalTerminalTab as SharedLocalTerminalTab } from '@skyhook-io/k8s-ui'
import { getWsUrl } from '../../api/config'

interface LocalTerminalTabProps {
  isActive?: boolean
  initialCommand?: string
}

export function LocalTerminalTab({ isActive, initialCommand }: LocalTerminalTabProps) {
  const createSession = () =>
    Promise.resolve({
      wsUrl: getWsUrl('/local-terminal'),
    })

  return (
    <SharedLocalTerminalTab
      isActive={isActive}
      createSession={createSession}
      initialCommand={initialCommand}
    />
  )
}
