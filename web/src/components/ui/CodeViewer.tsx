import { type ComponentProps } from 'react'
import { CodeViewer as BaseCodeViewer } from '@skyhook-io/k8s-ui'
import { useTheme } from '../../context/ThemeContext'

export function CodeViewer(props: Omit<ComponentProps<typeof BaseCodeViewer>, 'theme'>) {
  const { theme } = useTheme()
  return <BaseCodeViewer {...props} theme={theme} />
}
