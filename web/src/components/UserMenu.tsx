import { useState, useRef, useEffect, useCallback } from 'react'
import { User, LogOut } from 'lucide-react'
import { clsx } from 'clsx'
import { useAuthMe } from '../api/client'
import { Tooltip } from './ui/Tooltip'
import { useQueryClient } from '@tanstack/react-query'

interface UserMenuProps {
  // 'topbar' (default): 27px avatar, dropdown opens downward.
  // 'rail': a rail-bottom row (avatar + username, fly-out when slim), dropdown
  // opens UPWARD + escapes the narrow column to the right.
  variant?: 'topbar' | 'rail'
  /** Rail variant only: expanded (labels) vs slim (icon + fly-out). */
  pinned?: boolean
}

export function UserMenu({ variant = 'topbar', pinned = true }: UserMenuProps = {}) {
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
    let redirectTo = '/'
    try {
      const res = await fetch('/auth/logout', { credentials: 'same-origin' })
      const data = await res.json()
      if (data.redirectTo) {
        redirectTo = data.redirectTo
      }
    } catch (err) {
      console.error('[logout] Failed to complete server-side logout:', err)
    }
    queryClient.clear()
    window.location.href = redirectTo
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

  const isRail = variant === 'rail'
  const avatar = (
    <span className="w-7 h-7 rounded-full bg-blue-500/15 text-blue-500 flex items-center justify-center text-xs font-medium shrink-0">
      {initials || <User className="w-3.5 h-3.5" />}
    </span>
  )

  return (
    <div ref={menuRef} className={clsx('relative', isRail && 'group/item', isRail && !pinned && 'w-10')}>
      {isRail ? (
        <Tooltip content={authMe.username} position="right" wrapperClassName="!block w-full">
        <button
          onClick={() => setIsOpen(!isOpen)}
          className={clsx(
            'relative flex h-9 w-full items-center rounded-md text-sm font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary transition-colors',
            !pinned && 'max-w-10 overflow-hidden',
          )}
        >
          <span className="flex w-10 shrink-0 items-center justify-center">{avatar}</span>
          <span className={clsx('pr-3 truncate', !pinned && 'opacity-0')}>{authMe.username.split('@')[0]}</span>
        </button>
        </Tooltip>
      ) : (
        <Tooltip content={authMe.username}>
        <button
          onClick={() => setIsOpen(!isOpen)}
          className="w-7 h-7 rounded-full bg-blue-500/15 text-blue-500 flex items-center justify-center text-xs font-medium hover:bg-blue-500/25 transition-colors"
        >
          {initials || <User className="w-3.5 h-3.5" />}
        </button>
        </Tooltip>
      )}

      {/* Slim-rail fly-out label (account row, collapsed) */}
      {isRail && !pinned && !isOpen && (
        <span
          aria-hidden
          className="pointer-events-none absolute left-full top-1/2 z-50 ml-1 hidden -translate-y-1/2 whitespace-nowrap rounded-md border border-theme-border bg-theme-hover px-2.5 py-1 text-[13px] font-medium text-theme-text-primary opacity-0 shadow-lg shadow-black/30 transition-opacity duration-75 group-hover/item:block group-hover/item:opacity-100"
        >
          Account
        </span>
      )}

      {isOpen && (
        <div className={clsx(
          'absolute w-56 bg-theme-surface border border-theme-border rounded-lg shadow-lg z-50 py-1',
          // Rail: open UP (it sits at the viewport bottom) and align to the rail's
          // left edge so a 56px slim column doesn't clip it (it extends right).
          isRail ? 'bottom-full left-2 mb-1.5' : 'right-0 top-full mt-1.5',
        )}>
          <div className="px-3 py-2 border-b border-theme-border">
            <p className="text-sm font-medium text-theme-text-primary truncate">{authMe.username}</p>
            {authMe.groups && authMe.groups.length > 0 && (
              <p className="text-[11px] text-theme-text-tertiary mt-0.5 truncate">
                {authMe.groups.join(', ')}
              </p>
            )}
          </div>
          <button
            onClick={handleLogout}
            className="w-full flex items-center gap-2 px-3 py-1.5 text-sm text-theme-text-secondary hover:bg-theme-hover transition-colors"
          >
            <LogOut className="w-3.5 h-3.5" />
            Logout
          </button>
          {authMe.authMode === 'proxy' && !authMe.proxyLogoutConfigured && (
            <p className="px-3 py-1.5 text-[11px] text-theme-text-tertiary border-t border-theme-border">
              Logout clears the Radar session. The auth proxy may sign you back in automatically.
            </p>
          )}
        </div>
      )}
    </div>
  )
}
