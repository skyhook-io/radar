import { useState, useEffect, useRef, useCallback } from 'react'
import { Copy, Check, ChevronUp, ChevronDown, X, Search } from 'lucide-react'
import { codeToHtml } from 'shiki'

interface CodeViewerProps {
  code: string
  language?: 'yaml' | 'json' | 'bash' | 'text'
  /** Theme for syntax highlighting. Defaults to 'dark'. */
  theme?: 'dark' | 'light'
  showLineNumbers?: boolean
  maxHeight?: string
  showCopyButton?: boolean
  onCopy?: (text: string) => void
  copied?: boolean
}

export function CodeViewer({
  code,
  language = 'yaml',
  theme = 'dark',
  showLineNumbers = true,
  maxHeight = 'calc(100vh - 300px)',
  showCopyButton = false,
  onCopy,
  copied = false,
}: CodeViewerProps) {
  const [html, setHtml] = useState<string>('')
  const [highlighting, setHighlighting] = useState(true)

  // Search state
  const [searchOpen, setSearchOpen] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [searchDisplay, setSearchDisplay] = useState({ matchCount: 0, currentIndex: 0 })

  // Use refs for match tracking to avoid re-renders that destroy DOM highlights
  const matchCountRef = useRef(0)
  const currentIndexRef = useRef(0)

  const wrapperRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)
  const isMouseInsideRef = useRef(false)
  const htmlRef = useRef<string>('')

  useEffect(() => {
    if (!code) {
      setHtml('')
      setHighlighting(false)
      return
    }

    setHighlighting(true)

    const shikiLang = language === 'text' ? 'text' : language

    codeToHtml(code, {
      lang: shikiLang,
      theme: theme === 'light' ? 'github-light' : 'github-dark',
    })
      .then((highlighted) => {
        setHtml(highlighted)
        htmlRef.current = highlighted
        setHighlighting(false)
      })
      .catch((error) => {
        console.error('Shiki highlighting failed:', error)
        const escaped = code.replace(/</g, '&lt;').replace(/>/g, '&gt;')
        const fallback = `<pre><code>${escaped}</code></pre>`
        setHtml(fallback)
        htmlRef.current = fallback
        setHighlighting(false)
      })
  }, [code, language, theme])

  // Set innerHTML via ref so React doesn't manage it (preserves our <mark> elements across re-renders)
  useEffect(() => {
    if (contentRef.current && !highlighting && html) {
      contentRef.current.innerHTML = html
    }
  }, [html, highlighting])

  // Update the search display state (triggers re-render only for the search bar UI)
  const updateSearchDisplay = useCallback((count: number, index: number) => {
    setSearchDisplay({ matchCount: count, currentIndex: index })
  }, [])

  // Clear all <mark> highlights from the content, restore original HTML
  const clearHighlights = useCallback(() => {
    if (contentRef.current && htmlRef.current) {
      contentRef.current.innerHTML = htmlRef.current
    }
    matchCountRef.current = 0
    currentIndexRef.current = 0
    updateSearchDisplay(0, 0)
  }, [updateSearchDisplay])

  // Walk text nodes and wrap matches in <mark> elements
  const applyHighlights = useCallback((query: string, activeIndex: number) => {
    const container = contentRef.current
    if (!container || !query) {
      clearHighlights()
      return
    }

    // Restore original HTML first (clean slate)
    if (htmlRef.current) {
      container.innerHTML = htmlRef.current
    }

    const lowerQuery = query.toLowerCase()
    let totalMatches = 0

    // Collect all text nodes
    const textNodes: Text[] = []
    const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT)
    let node: Text | null
    while ((node = walker.nextNode() as Text | null)) {
      if (node.textContent && node.textContent.length > 0) {
        textNodes.push(node)
      }
    }

    // Process each text node — find matches and wrap them
    for (const textNode of textNodes) {
      const text = textNode.textContent || ''
      const lowerText = text.toLowerCase()

      // Find all match positions in this text node
      const positions: number[] = []
      let searchFrom = 0
      while (searchFrom < lowerText.length) {
        const idx = lowerText.indexOf(lowerQuery, searchFrom)
        if (idx === -1) break
        positions.push(idx)
        searchFrom = idx + 1
      }

      if (positions.length === 0) continue

      const parent = textNode.parentNode
      if (!parent) continue

      const frag = document.createDocumentFragment()
      let lastEnd = 0

      for (const pos of positions) {
        // Text before match
        if (pos > lastEnd) {
          frag.appendChild(document.createTextNode(text.slice(lastEnd, pos)))
        }

        // The match
        const mark = document.createElement('mark')
        mark.className = totalMatches === activeIndex
          ? 'search-highlight active'
          : 'search-highlight'
        mark.textContent = text.slice(pos, pos + query.length)
        mark.dataset.matchIndex = String(totalMatches)
        frag.appendChild(mark)

        totalMatches++
        lastEnd = pos + query.length
      }

      // Remaining text
      if (lastEnd < text.length) {
        frag.appendChild(document.createTextNode(text.slice(lastEnd)))
      }

      parent.replaceChild(frag, textNode)
    }

    matchCountRef.current = totalMatches
    const clampedIndex = totalMatches > 0 ? Math.min(activeIndex, totalMatches - 1) : 0
    currentIndexRef.current = clampedIndex
    updateSearchDisplay(totalMatches, clampedIndex)

    // Scroll to active match
    if (totalMatches > 0) {
      requestAnimationFrame(() => {
        const activeMark = container.querySelector(`mark.search-highlight[data-match-index="${clampedIndex}"]`)
        if (activeMark) {
          activeMark.scrollIntoView({ block: 'center', behavior: 'smooth' })
        }
      })
    }
  }, [clearHighlights, updateSearchDisplay])

  // Update active match by swapping CSS classes (no DOM rebuild)
  const setActiveMatch = useCallback((newIndex: number) => {
    const container = contentRef.current
    if (!container || matchCountRef.current === 0) return

    // Remove active from current
    const current = container.querySelector('mark.search-highlight.active')
    if (current) {
      current.className = 'search-highlight'
    }

    // Add active to new
    const next = container.querySelector(`mark.search-highlight[data-match-index="${newIndex}"]`)
    if (next) {
      next.className = 'search-highlight active'
      next.scrollIntoView({ block: 'center', behavior: 'smooth' })
    }

    currentIndexRef.current = newIndex
    updateSearchDisplay(matchCountRef.current, newIndex)
  }, [updateSearchDisplay])

  // Apply highlights when query changes
  useEffect(() => {
    if (!searchOpen) return
    if (!searchQuery) {
      clearHighlights()
      return
    }

    const timer = setTimeout(() => {
      applyHighlights(searchQuery, 0)
    }, 150)

    return () => clearTimeout(timer)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchQuery, searchOpen])

  // Re-apply highlights when Shiki finishes while search is open
  useEffect(() => {
    if (searchOpen && searchQuery && !highlighting) {
      const timer = setTimeout(() => {
        applyHighlights(searchQuery, currentIndexRef.current)
      }, 50)
      return () => clearTimeout(timer)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [html, highlighting])

  // Close search when code changes
  useEffect(() => {
    if (searchOpen) {
      clearHighlights()
      setSearchOpen(false)
      setSearchQuery('')
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [code])

  // Navigation
  const goToNextMatch = useCallback(() => {
    if (matchCountRef.current === 0) return
    const newIndex = (currentIndexRef.current + 1) % matchCountRef.current
    setActiveMatch(newIndex)
  }, [setActiveMatch])

  const goToPrevMatch = useCallback(() => {
    if (matchCountRef.current === 0) return
    const newIndex = (currentIndexRef.current - 1 + matchCountRef.current) % matchCountRef.current
    setActiveMatch(newIndex)
  }, [setActiveMatch])

  // Open search
  const openSearch = useCallback(() => {
    setSearchOpen(true)
    requestAnimationFrame(() => {
      searchInputRef.current?.focus()
      searchInputRef.current?.select()
    })
  }, [])

  // Close search
  const closeSearch = useCallback(() => {
    setSearchOpen(false)
    setSearchQuery('')
    clearHighlights()
  }, [clearHighlights])

  // Keyboard: Ctrl+F on wrapper
  const handleWrapperKeyDown = useCallback((e: React.KeyboardEvent) => {
    if ((e.ctrlKey || e.metaKey) && e.key === 'f') {
      e.preventDefault()
      e.stopPropagation()
      openSearch()
    }
  }, [openSearch])

  // Global Ctrl+F when mouse is inside
  useEffect(() => {
    const handleGlobalKeyDown = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 'f' && isMouseInsideRef.current) {
        e.preventDefault()
        e.stopPropagation()
        openSearch()
      }
    }
    document.addEventListener('keydown', handleGlobalKeyDown, true)
    return () => document.removeEventListener('keydown', handleGlobalKeyDown, true)
  }, [openSearch])

  // Search input keyboard
  const handleSearchKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      closeSearch()
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (e.shiftKey) {
        goToPrevMatch()
      } else {
        goToNextMatch()
      }
    }
  }, [closeSearch, goToNextMatch, goToPrevMatch])

  const handleCopy = () => {
    if (onCopy) {
      onCopy(code)
    } else {
      navigator.clipboard.writeText(code)
    }
  }

  return (
    <div
      ref={wrapperRef}
      className={`relative rounded-lg overflow-hidden ${theme === 'light' ? 'bg-[#ffffff]' : 'bg-[#0d1117]'}`}
      tabIndex={0}
      onKeyDown={handleWrapperKeyDown}
      onMouseEnter={() => { isMouseInsideRef.current = true }}
      onMouseLeave={() => { isMouseInsideRef.current = false }}
      style={{ outline: 'none' }}
    >
      {showCopyButton && !searchOpen && (
        <button
          onClick={handleCopy}
          className="absolute top-2 right-2 z-10 flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary bg-theme-surface/80 hover:bg-theme-elevated rounded backdrop-blur-sm"
        >
          {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
          Copy
        </button>
      )}

      {/* Search bar */}
      {searchOpen && (
        <div className="absolute top-2 right-2 z-20 flex items-center gap-1 px-2 py-1.5 bg-theme-surface border border-theme-border rounded-lg shadow-lg backdrop-blur-sm">
          <Search className="w-3.5 h-3.5 text-theme-text-tertiary shrink-0" />
          <input
            ref={searchInputRef}
            type="text"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            onKeyDown={handleSearchKeyDown}
            placeholder="Search..."
            className="w-40 bg-transparent text-xs text-theme-text-primary placeholder:text-theme-text-disabled outline-none"
            autoFocus
          />
          {searchQuery && (
            <span className="text-xs text-theme-text-tertiary whitespace-nowrap shrink-0">
              {searchDisplay.matchCount > 0
                ? `${searchDisplay.currentIndex + 1} of ${searchDisplay.matchCount}`
                : 'No results'}
            </span>
          )}
          <button
            onClick={goToPrevMatch}
            disabled={searchDisplay.matchCount === 0}
            className="p-0.5 text-theme-text-tertiary hover:text-theme-text-primary disabled:opacity-30 rounded"
            title="Previous (Shift+Enter)"
          >
            <ChevronUp className="w-3.5 h-3.5" />
          </button>
          <button
            onClick={goToNextMatch}
            disabled={searchDisplay.matchCount === 0}
            className="p-0.5 text-theme-text-tertiary hover:text-theme-text-primary disabled:opacity-30 rounded"
            title="Next (Enter)"
          >
            <ChevronDown className="w-3.5 h-3.5" />
          </button>
          <button
            onClick={closeSearch}
            className="p-0.5 text-theme-text-tertiary hover:text-theme-text-primary rounded"
            title="Close (Escape)"
          >
            <X className="w-3.5 h-3.5" />
          </button>
        </div>
      )}

      <div
        className="overflow-auto"
        style={{ maxHeight }}
      >
        {highlighting ? (
          <div className="p-4 text-theme-text-tertiary text-sm font-mono">Loading...</div>
        ) : (
          <div
            ref={contentRef}
            className={`shiki-viewer text-xs ${showLineNumbers ? 'with-line-numbers' : ''}`}
          />
        )}
      </div>

      <style>{`
        :root { --code-line-number: light-dark(#8b949e, #484f58); }
        .shiki-viewer pre {
          margin: 0;
          padding: 12px;
          background: transparent !important;
          font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
          font-size: 12px;
          line-height: 1.5;
        }
        .shiki-viewer code {
          font-family: inherit;
        }
        .shiki-viewer.with-line-numbers code {
          counter-reset: line;
        }
        .shiki-viewer .line {
          display: inline-block;
          width: 100%;
        }
        .shiki-viewer.with-line-numbers .line::before {
          counter-increment: line;
          content: counter(line);
          display: inline-block;
          width: 3ch;
          margin-right: 1.5ch;
          text-align: right;
          color: var(--code-line-number, #484f58);
          user-select: none;
        }
        .shiki-viewer .line:hover {
          background: rgba(99, 110, 123, 0.1);
        }
        mark.search-highlight {
          background-color: rgba(250, 204, 21, 0.35);
          color: inherit;
          border-radius: 2px;
          padding: 0;
        }
        mark.search-highlight.active {
          background-color: rgba(250, 204, 21, 0.8);
          outline: 2px solid rgba(250, 204, 21, 0.9);
          border-radius: 2px;
        }
      `}</style>
    </div>
  )
}
