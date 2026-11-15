// Title bar for the Windows desktop app. The window is frameless there and the
// page draws the icon, menus, title and window buttons in one row; see
// cmd/desktop/titlebar.go. It activates only inside WebView2 once the desktop
// app answers the hello, so browsers, Radar Hub and the macOS / Linux apps
// never render it.
//
// The page is served by Radar's local server, not the Wails origin, so the
// Wails JS runtime isn't injected. The bar posts raw Wails messages instead:
// "drag", "resize:<edge>", "Wm" (minimise), "Wt" (toggle maximise), "Q" (quit)
// and "EE{...}" events.
import { useCallback, useEffect, useRef, useState } from 'react'
import ReactDOM from 'react-dom/client'
import { clsx } from 'clsx'
import { useAnimatedUnmount } from '../hooks/useAnimatedUnmount'
import { TRANSITION_MENU, overlayExitMs, overlayTransitionStyle } from '../utils/animation'
import {
  type Press,
  type TitleBarMenu,
  type TitleBarState,
  isDoublePress,
  looksMaximised,
  mnemonicMenuIndex,
  resizeEdgeAt,
} from './titleBar'

const TITLEBAR_HEIGHT = 32
const HELLO_INTERVAL_MS = 250
const HELLO_TRIES = 120

type WebView = { postMessage: (message: string) => void }

function webview(): WebView | null {
  return (window as unknown as { chrome?: { webview?: WebView } }).chrome?.webview ?? null
}

function post(message: string) {
  webview()?.postMessage(message)
}

function emit(name: string, ...data: unknown[]) {
  post('EE' + JSON.stringify({ name, data }))
}

/** True inside the Windows desktop app, where the page can run native menu items. */
export function hasDesktopMenu(): boolean {
  return webview() !== null
}

/** Runs a native menu item by its "Menu/Label" path (cmd/desktop/titlebar.go). */
export function runDesktopMenuItem(path: string) {
  emit('radar:titlebar:invoke', path)
}

function isMaximised() {
  return looksMaximised(window.outerWidth, window.outerHeight, screen.availWidth, screen.availHeight)
}

// Caption glyphs: Segoe Fluent Icons on Windows 11, Segoe MDL2 Assets on Windows 10.
const GLYPH = { minimise: '', maximise: '', restore: '', close: '' }

function TitleBar({ title, menus }: TitleBarState) {
  const [open, setOpen] = useState<number | null>(null)
  const [maximised, setMaximised] = useState(isMaximised)
  const lastPress = useRef<Press>({ t: 0, x: 0, y: 0 })
  const triggers = useRef<(HTMLButtonElement | null)[]>([])
  // Where focus was before Alt or a mnemonic moved it into the bar.
  const returnFocus = useRef<HTMLElement | null>(null)

  useEffect(() => {
    const onResize = () => setMaximised(isMaximised())
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  const leaveBar = useCallback(() => {
    setOpen(null)
    const prev = returnFocus.current
    returnFocus.current = null
    if (prev?.isConnected) prev.focus()
    else (document.activeElement as HTMLElement | null)?.blur()
  }, [])

  const enterBar = useCallback(() => {
    if (!(document.activeElement as HTMLElement | null)?.closest?.('[data-radar-titlebar]')) {
      returnFocus.current = document.activeElement as HTMLElement | null
    }
  }, [])

  const invoke = useCallback((path: string) => {
    leaveBar()
    runDesktopMenuItem(path)
  }, [leaveBar])

  useEffect(() => {
    let altAlone = false
    const inBar = () => !!(document.activeElement as HTMLElement | null)?.closest?.('[data-radar-titlebar]')
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Alt') {
        altAlone = !e.repeat
        return
      }
      altAlone = false
      const mnemonic = mnemonicMenuIndex(menus, e)
      if (mnemonic >= 0) {
        e.preventDefault()
        enterBar()
        triggers.current[mnemonic]?.focus()
        setOpen(mnemonic)
        return
      }
      // Consume Escape here, at the first capture listener, so drawers and
      // dialogs underneath don't also close.
      if (e.key === 'Escape' && (open !== null || inBar())) {
        e.preventDefault()
        e.stopPropagation()
        if (open !== null) {
          const i = open
          setOpen(null)
          triggers.current[i]?.focus()
        } else {
          leaveBar()
        }
      }
    }
    const onKeyUp = (e: KeyboardEvent) => {
      if (e.key !== 'Alt' || !altAlone) return
      altAlone = false
      e.preventDefault()
      if (inBar()) {
        leaveBar()
      } else {
        enterBar()
        triggers.current[0]?.focus()
      }
    }
    window.addEventListener('keydown', onKeyDown, true)
    window.addEventListener('keyup', onKeyUp, true)
    return () => {
      window.removeEventListener('keydown', onKeyDown, true)
      window.removeEventListener('keyup', onKeyUp, true)
    }
  }, [menus, open, enterBar, leaveBar])

  useEffect(() => {
    if (open === null) return
    const onDown = (e: MouseEvent) => {
      if (!(e.target as HTMLElement).closest?.('[data-radar-titlebar]')) setOpen(null)
    }
    window.addEventListener('mousedown', onDown, true)
    return () => window.removeEventListener('mousedown', onDown, true)
  }, [open])

  const onBarMouseDown = (e: React.MouseEvent) => {
    if (e.button !== 0 || (e.target as HTMLElement).closest('[data-no-drag]')) return
    const press = { t: performance.now(), x: e.screenX, y: e.screenY }
    const double = isDoublePress(lastPress.current, press)
    lastPress.current = double ? { t: 0, x: 0, y: 0 } : press
    setOpen(null)
    post(double ? 'Wt' : 'drag')
  }

  const moveTo = (j: number) => {
    triggers.current[j]?.focus()
    if (open !== null) setOpen(j)
  }

  const onTriggerKey = (i: number) => (e: React.KeyboardEvent) => {
    const n = menus.length
    if (e.key === 'ArrowRight') { e.preventDefault(); moveTo((i + 1) % n) }
    else if (e.key === 'ArrowLeft') { e.preventDefault(); moveTo((i + n - 1) % n) }
    else if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setOpen(i) }
  }

  return (
    <div
      data-radar-titlebar
      onMouseDown={onBarMouseDown}
      className="fixed inset-x-0 top-0 flex items-stretch select-none text-xs bg-theme-base border-b border-theme-border/50 text-theme-text-secondary"
      style={{ height: TITLEBAR_HEIGHT, zIndex: 2147483000, fontFamily: '"Segoe UI Variable Text", "Segoe UI", system-ui, sans-serif' }}
    >
      <div className="flex items-center pl-3 pr-1">
        <img src="/favicon.svg" alt="" width={16} height={16} draggable={false} />
      </div>

      <div className="flex items-stretch" data-no-drag role="menubar" aria-label="Application menu">
        {menus.map((m, i) => (
          <div key={m.label} className="relative flex">
            <button
              ref={(el) => { triggers.current[i] = el }}
              type="button"
              role="menuitem"
              aria-haspopup="menu"
              aria-expanded={open === i}
              className={clsx(
                'px-2.5 my-1 rounded outline-none hover:bg-theme-hover hover:text-theme-text-primary focus-visible:bg-theme-hover focus-visible:text-theme-text-primary',
                open === i && 'bg-theme-hover text-theme-text-primary',
              )}
              // Keep focus (and any text selection) where it is, so Edit → Copy / Paste act on it.
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => { if (open === i) { setOpen(null) } else { enterBar(); setOpen(i) } }}
              onMouseEnter={() => { if (open !== null && open !== i) setOpen(i) }}
              onKeyDown={onTriggerKey(i)}
            >
              {m.label}
            </button>
            <MenuDropdown
              menu={m}
              open={open === i}
              onInvoke={invoke}
              onMove={(dir) => moveTo((i + dir + menus.length) % menus.length)}
            />
          </div>
        ))}
      </div>

      <div className="flex-1 min-w-0 flex items-center justify-center px-4">
        <span className="truncate">{title}</span>
      </div>

      <div className="flex items-stretch" data-no-drag>
        <CaptionButton glyph={GLYPH.minimise} label="Minimize" onClick={() => post('Wm')} />
        <CaptionButton glyph={maximised ? GLYPH.restore : GLYPH.maximise} label={maximised ? 'Restore' : 'Maximize'} onClick={() => post('Wt')} />
        <CaptionButton glyph={GLYPH.close} label="Close" onClick={() => post('Q')} close />
      </div>
    </div>
  )
}

function MenuDropdown({ menu, open, onInvoke, onMove }: { menu: TitleBarMenu; open: boolean; onInvoke: (path: string) => void; onMove: (dir: 1 | -1) => void }) {
  const { shouldRender, isOpen } = useAnimatedUnmount(open, overlayExitMs('menu'))
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (open && shouldRender) ref.current?.querySelector<HTMLButtonElement>('button')?.focus({ preventScroll: true })
  }, [open, shouldRender])

  if (!shouldRender) return null

  const onKey = (e: React.KeyboardEvent) => {
    const items = Array.from(ref.current?.querySelectorAll<HTMLButtonElement>('button') ?? [])
    const idx = items.indexOf(document.activeElement as HTMLButtonElement)
    if (e.key === 'ArrowDown') { e.preventDefault(); items[(idx + 1) % items.length]?.focus() }
    else if (e.key === 'ArrowUp') { e.preventDefault(); items[(idx - 1 + items.length) % items.length]?.focus() }
    else if (e.key === 'ArrowRight') { e.preventDefault(); onMove(1) }
    else if (e.key === 'ArrowLeft') { e.preventDefault(); onMove(-1) }
  }

  return (
    <div
      ref={ref}
      role="menu"
      aria-label={menu.label}
      inert={!open}
      onKeyDown={onKey}
      className={clsx(
        'absolute left-0 top-full mt-0.5 min-w-[220px] py-1 rounded-md origin-top-left',
        'border border-theme-border bg-theme-elevated shadow-theme-md text-theme-text-primary',
        TRANSITION_MENU,
        isOpen ? 'opacity-100 translate-y-0 scale-100' : 'opacity-0 -translate-y-1 scale-[0.97]',
        !open && 'pointer-events-none',
      )}
      style={overlayTransitionStyle(isOpen, 'menu')}
    >
      {menu.items.map((item, i) =>
        item.separator ? (
          <div key={i} role="separator" className="my-1 border-t border-theme-border" />
        ) : (
          <button
            key={item.label}
            type="button"
            role="menuitem"
            className="w-full flex items-center justify-between gap-6 px-3 py-1.5 text-left outline-none hover:bg-theme-hover focus:bg-theme-hover"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onInvoke(menu.label + '/' + item.label)}
          >
            <span>{item.label}</span>
            {item.shortcut && <span className="text-theme-text-tertiary">{item.shortcut}</span>}
          </button>
        ),
      )}
    </div>
  )
}

function CaptionButton({ glyph, label, onClick, close }: { glyph: string; label: string; onClick: () => void; close?: boolean }) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onMouseDown={(e) => e.preventDefault()}
      onClick={onClick}
      className={clsx(
        'w-[46px] text-[10px] outline-none',
        // Red on hover is the Windows close-button convention, not a theme colour.
        close ? 'hover:bg-[#c42b1c] hover:text-white' : 'hover:bg-theme-hover hover:text-theme-text-primary',
      )}
      style={{ fontFamily: '"Segoe Fluent Icons", "Segoe MDL2 Assets"' }}
    >
      {glyph}
    </button>
  )
}

// A frameless window has no native resize border, so the page edges start the resize.
function installResizeEdges() {
  const edgeFor = (e: MouseEvent) => resizeEdgeAt(e.clientX, e.clientY, window.innerWidth, window.innerHeight, isMaximised())
  let cursor: string | null = null
  const setCursor = (next: string | null) => {
    if (next === cursor) return
    cursor = next
    document.documentElement.style.cursor = next ?? ''
  }
  window.addEventListener('mousemove', (e) => setCursor(edgeFor(e)), true)
  window.addEventListener('resize', () => setCursor(null))
  window.addEventListener('blur', () => setCursor(null))
  // Recompute from this press: the window may have been maximised or resized
  // since the pointer last moved.
  window.addEventListener('mousedown', (e) => {
    const edge = e.button === 0 ? edgeFor(e) : null
    if (!edge) return
    e.preventDefault()
    e.stopPropagation()
    post('resize:' + edge)
  }, true)
}

export function installWindowsTitleBar() {
  if (!webview()) return
  let root: ReactDOM.Root | null = null
  let rev = -1
  window.addEventListener('radar:titlebar', (e) => {
    const detail = (e as CustomEvent<TitleBarState & { rev: number }>).detail
    // Pushes can arrive out of order; an older one would show a stale title.
    if (detail.rev <= rev) return
    rev = detail.rev
    const { title, menus } = detail
    if (!root) {
      document.documentElement.classList.add('radar-custom-titlebar')
      document.documentElement.style.setProperty('--radar-titlebar-h', `${TITLEBAR_HEIGHT}px`)
      const host = document.createElement('div')
      document.body.prepend(host)
      root = ReactDOM.createRoot(host)
      installResizeEdges()
    }
    root.render(<TitleBar title={title || 'Radar'} menus={menus ?? []} />)
  })
  // The desktop app registers its handler at startup, which can finish after
  // the page loads; repeat the hello until the first answer arrives.
  let tries = 0
  const hello = () => {
    if (root || tries++ >= HELLO_TRIES) return
    emit('radar:titlebar:hello')
    window.setTimeout(hello, HELLO_INTERVAL_MS)
  }
  hello()
}
