import React from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider, MutationCache, QueryCache } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import { ToastProvider, showApiError, showApiSuccess } from './components/ui/Toast'
import { ThemeProvider } from './context/ThemeContext'
import './index.css'

// Type the meta property for mutations
declare module '@tanstack/react-query' {
  interface Register {
    mutationMeta: {
      errorMessage?: string      // e.g., "Failed to delete resource"
      successMessage?: string    // e.g., "Resource deleted"
      successDetail?: string     // e.g., "Pod 'nginx' removed"
    }
  }
}

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
  mutationCache: new MutationCache({
    onError: (error, _variables, _context, mutation) => {
      // Only show toast if errorMessage is explicitly provided in meta
      // This allows mutations to opt-out by not providing meta (e.g., context switch has its own dialog)
      const message = mutation.options.meta?.errorMessage
      if (message) {
        showApiError(message, error.message)
      }
    },
    onSuccess: (_data, _variables, _context, mutation) => {
      const message = mutation.options.meta?.successMessage
      if (message) {
        showApiSuccess(message, mutation.options.meta?.successDetail)
      }
    },
  }),
  queryCache: new QueryCache({
    onError: (error, query) => {
      // Log background refetch failures (when stale data exists)
      if (query.state.data !== undefined) {
        console.warn('[Background sync failed]', query.queryKey, error.message)
      }
    },
  }),
})

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter>
      <ThemeProvider>
        <QueryClientProvider client={queryClient}>
          <ToastProvider>
            <App />
          </ToastProvider>
        </QueryClientProvider>
      </ThemeProvider>
    </BrowserRouter>
  </React.StrictMode>
)
