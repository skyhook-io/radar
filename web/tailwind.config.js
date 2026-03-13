import typography from '@tailwindcss/typography'

/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
    "../packages/k8s-ui/src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    fontFamily: {
      sans: ['"DM Sans Variable"', '"DM Sans"', 'system-ui', 'sans-serif'],
      mono: ['"DM Mono"', 'ui-monospace', 'SFMono-Regular', 'Menlo', 'monospace'],
    },
    screens: {
      'sm': '900px',
      'md': '1100px',
      'lg': '1280px',
      'xl': '1536px',
    },
    extend: {
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
      borderRadius: {
        'lg': '0.625rem',   /* 10px (was 8px) */
        'xl': '0.875rem',   /* 14px (was 12px) */
      },
      ringOffsetColor: {
        'theme-base': 'var(--bg-base)',
      },
    },
  },
  plugins: [typography],
}
