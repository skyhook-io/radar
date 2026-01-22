/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        // Theme-aware colors (use CSS variables)
        'theme': {
          'base': 'var(--bg-base)',
          'surface': 'var(--bg-surface)',
          'elevated': 'var(--bg-elevated)',
          'hover': 'var(--bg-hover)',
          'active': 'var(--bg-active)',
        },
        'theme-text': {
          'primary': 'var(--text-primary)',
          'secondary': 'var(--text-secondary)',
          'tertiary': 'var(--text-tertiary)',
          'disabled': 'var(--text-disabled)',
        },
        'theme-border': {
          DEFAULT: 'var(--border-default)',
          'light': 'var(--border-light)',
          'subtle': 'var(--border-subtle)',
        },
        'accent': {
          DEFAULT: 'var(--accent)',
          'light': 'var(--accent-light)',
          'muted': 'var(--accent-muted)',
          'text': 'var(--accent-text)',
        },
        // Skyhook brand colors
        'skyhook': {
          DEFAULT: '#2D7AFF',
          50: '#E6F0FF',
          100: '#CCE0FF',
          200: '#99C2FF',
          300: '#66A3FF',
          400: '#3385FF',
          500: '#2D7AFF',
          600: '#0052CC',
          700: '#003D99',
          800: '#002966',
          900: '#001433',
        },
        // K8s resource type colors
        'k8s-internet': '#2D7AFF',
        'k8s-ingress': '#8b5cf6',
        'k8s-service': '#3b82f6',
        'k8s-deployment': '#10b981',
        'k8s-daemonset': '#14b8a6',
        'k8s-statefulset': '#06b6d4',
        'k8s-replicaset': '#22c55e',
        'k8s-pod': '#84cc16',
        'k8s-configmap': '#f59e0b',
        'k8s-secret': '#ef4444',
        'k8s-hpa': '#ec4899',
        'k8s-job': '#a855f7',
        'k8s-cronjob': '#d946ef',
      },
      boxShadow: {
        'theme-sm': 'var(--shadow-sm)',
        'theme-md': 'var(--shadow-md)',
        'theme-lg': 'var(--shadow-lg)',
      },
      animation: {
        'fade-in-out': 'fadeInOut 2s ease-in-out forwards',
      },
      keyframes: {
        fadeInOut: {
          '0%': { opacity: '0', transform: 'translateY(-8px)' },
          '15%': { opacity: '1', transform: 'translateY(0)' },
          '85%': { opacity: '1', transform: 'translateY(0)' },
          '100%': { opacity: '0', transform: 'translateY(-8px)' },
        },
      },
      ringColor: {
        'accent': 'var(--accent)',
      },
      ringOffsetColor: {
        'theme-base': 'var(--bg-base)',
      },
    },
  },
  plugins: [],
}
