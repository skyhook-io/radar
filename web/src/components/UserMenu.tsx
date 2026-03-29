import { useState, useRef, useEffect, useCallback } from 'react'
import { User, LogOut } from 'lucide-react'
import { useAuthMe } from '../api/client'
import { useQueryClient } from '@tanstack/react-query'

export function UserMenu() {
  const { data: authMe } = useAuthMe()
  const [isOpen, setIsOpen] = useState(false)
  const menuRef = useRef<HTMLDivElement>(null)
  const queryClient = useQueryClient()

  // Close on click outside
  useEffect(() => {
    if (!isOpen) return
    function handleClick(e: MouseEvent) {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setIsOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [isOpen])

  const handleLogout = useCallback(async () => {
    try {
      await fetch('/auth/logout', { credentials: 'same-origin' })
    } catch {
      // Cookie may already be cleared
    }
    // Clear all cached data and redirect — auth barrier will handle login redirect
    queryClient.clear()
    window.location.href = '/'
  }, [queryClient])

  if (!authMe?.authEnabled || !authMe?.username) {
    return null
  }

  const initials = authMe.username
    .split('@')[0]
    .split(/[._-]/)
    .slice(0, 2)
    .map(s => s[0]?.toUpperCase() || '')
    .join('')

  return (
    <div ref={menuRef} className="relative">
      <button
        onClick={() => setIsOpen(!isOpen)}
        className="w-7 h-7 rounded-full bg-blue-500/15 text-blue-500 flex items-center justify-center text-xs font-medium hover:bg-blue-500/25 transition-colors"
        title={authMe.username}
      >
        {initials || <User className="w-3.5 h-3.5" />}
      </button>

      {isOpen && (
        <div className="absolute right-0 top-full mt-1.5 w-56 bg-theme-surface border border-theme-border rounded-lg shadow-lg z-50 py-1">
          <div className="px-3 py-2 border-b border-theme-border">
            <p className="text-sm font-medium text-theme-text-primary truncate">{authMe.username}</p>
            {authMe.groups && authMe.groups.length > 0 && (
              <p className="text-[11px] text-theme-text-tertiary mt-0.5 truncate">
                {authMe.groups.join(', ')}
              </p>
            )}
          </div>
          {authMe.authMode === 'proxy' ? (
            <p className="px-3 py-1.5 text-[11px] text-theme-text-tertiary">
              Session managed by auth proxy
            </p>
          ) : (
            <button
              onClick={handleLogout}
              className="w-full flex items-center gap-2 px-3 py-1.5 text-sm text-theme-text-secondary hover:bg-theme-hover transition-colors"
            >
              <LogOut className="w-3.5 h-3.5" />
              Logout
            </button>
          )}
        </div>
      )}
    </div>
  )
}
