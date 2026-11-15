// Pure helpers for the Windows desktop title bar (WindowsTitleBar.tsx).

export type TitleBarItem = { label?: string; shortcut?: string; separator?: boolean }
export type TitleBarMenu = { label: string; items: TitleBarItem[] }
export type TitleBarState = { title: string; menus: TitleBarMenu[] }

export const RESIZE_EDGE_PX = 6

/** Edge names are the cursor values Wails maps to resize hit-tests ("resize:<edge>"). */
export function resizeEdgeAt(x: number, y: number, width: number, height: number, maximised: boolean): string | null {
  if (maximised) return null
  const top = y < RESIZE_EDGE_PX
  const bottom = y >= height - RESIZE_EDGE_PX
  const left = x < RESIZE_EDGE_PX
  const right = x >= width - RESIZE_EDGE_PX
  if (top && left) return 'nw-resize'
  if (top && right) return 'ne-resize'
  if (bottom && left) return 'sw-resize'
  if (bottom && right) return 'se-resize'
  if (top) return 'n-resize'
  if (bottom) return 's-resize'
  if (left) return 'w-resize'
  if (right) return 'e-resize'
  return null
}

/** Alt+<first letter> opens that menu, as in native Windows menu bars. */
export function mnemonicMenuIndex(menus: TitleBarMenu[], e: Pick<KeyboardEvent, 'key' | 'altKey' | 'ctrlKey' | 'metaKey' | 'shiftKey'>): number {
  if (!e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || e.key.length !== 1) return -1
  const k = e.key.toLowerCase()
  return menus.findIndex((m) => m.label.charAt(0).toLowerCase() === k)
}

export type Press = { t: number; x: number; y: number }

/** The native move loop swallows the browser's click sequence, so a double
 *  click on the drag area is detected by time and distance instead. */
export function isDoublePress(prev: Press, next: Press, windowMs = 500, slopPx = 4): boolean {
  return next.t - prev.t < windowMs && Math.abs(next.x - prev.x) < slopPx && Math.abs(next.y - prev.y) < slopPx
}

/** A frameless window maximised by Windows fills the monitor's work area. */
export function looksMaximised(outerWidth: number, outerHeight: number, availWidth: number, availHeight: number): boolean {
  return outerWidth >= availWidth - 1 && outerHeight >= availHeight - 1
}
