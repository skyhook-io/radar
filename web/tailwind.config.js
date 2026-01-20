/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        // K8s resource type colors
        'k8s-internet': '#6366f1',
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
    },
  },
  plugins: [],
}
