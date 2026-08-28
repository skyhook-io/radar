import { Component, ReactNode } from 'react'
import { AlertTriangle, RefreshCw } from 'lucide-react'

interface Props {
  children: ReactNode
  /**
   * Clears a caught error whenever this value changes - pass whatever selects
   * the children, typically the route. A boundary latches: nothing resets
   * hasError on its own, so without this a view that throws keeps the fallback
   * on screen and navigating away cannot escape it. Preferred over remounting
   * via `key`, which would also discard the children's state on every change
   * while nothing is wrong.
   */
  resetKey: string | number
}

interface State {
  hasError: boolean
  error: Error | null
  resetKey: string | number
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { hasError: false, error: null, resetKey: this.props.resetKey }

  static getDerivedStateFromError(error: Error): Pick<State, 'hasError' | 'error'> {
    return { hasError: true, error }
  }

  static getDerivedStateFromProps(props: Props, state: State): State | null {
    if (Object.is(props.resetKey, state.resetKey)) return null
    return { hasError: false, error: null, resetKey: props.resetKey }
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    console.error('[ErrorBoundary]', error, info.componentStack)
  }

  handleReset = () => this.setState({ hasError: false, error: null })

  render() {
    if (!this.state.hasError) return this.props.children

    return (
      <div className="flex flex-col items-center justify-center h-full p-8 text-center">
        <AlertTriangle className="w-12 h-12 text-red-400 mb-4" />
        <h2 className="text-lg font-semibold text-theme-text-primary mb-2">Something went wrong</h2>
        <p className="text-sm text-theme-text-secondary mb-4 max-w-md">
          {this.state.error?.message || 'An unexpected error occurred'}
        </p>
        <button
          onClick={this.handleReset}
          className="flex items-center gap-2 px-4 py-2 text-sm font-medium btn-brand rounded-lg"
        >
          <RefreshCw className="w-4 h-4" />
          Try Again
        </button>
      </div>
    )
  }
}
