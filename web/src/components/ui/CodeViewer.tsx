import { useState, useEffect } from 'react'
import { Copy, Check } from 'lucide-react'
import { codeToHtml } from 'shiki'

interface CodeViewerProps {
  code: string
  language?: 'yaml' | 'json' | 'bash' | 'text'
  showLineNumbers?: boolean
  maxHeight?: string
  showCopyButton?: boolean
  onCopy?: (text: string) => void
  copied?: boolean
}

export function CodeViewer({
  code,
  language = 'yaml',
  showLineNumbers = true,
  maxHeight = 'calc(100vh - 300px)',
  showCopyButton = false,
  onCopy,
  copied = false,
}: CodeViewerProps) {
  const [html, setHtml] = useState<string>('')
  const [highlighting, setHighlighting] = useState(true)

  useEffect(() => {
    if (!code) {
      setHtml('')
      setHighlighting(false)
      return
    }

    setHighlighting(true)

    // Use 'text' language as fallback for unknown languages
    const shikiLang = language === 'text' ? 'text' : language

    codeToHtml(code, {
      lang: shikiLang,
      theme: 'github-dark',
    })
      .then((highlighted) => {
        setHtml(highlighted)
        setHighlighting(false)
      })
      .catch((error) => {
        console.error('Shiki highlighting failed:', error)
        // Fallback to plain text
        const escaped = code.replace(/</g, '&lt;').replace(/>/g, '&gt;')
        setHtml(`<pre><code>${escaped}</code></pre>`)
        setHighlighting(false)
      })
  }, [code, language])

  const handleCopy = () => {
    if (onCopy) {
      onCopy(code)
    } else {
      navigator.clipboard.writeText(code)
    }
  }

  return (
    <div className="relative rounded-lg overflow-hidden bg-[#0d1117]">
      {showCopyButton && (
        <button
          onClick={handleCopy}
          className="absolute top-2 right-2 z-10 flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary bg-theme-surface/80 hover:bg-theme-elevated rounded backdrop-blur-sm"
        >
          {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
          Copy
        </button>
      )}

      <div
        className="overflow-auto"
        style={{ maxHeight }}
      >
        {highlighting ? (
          <div className="p-4 text-theme-text-tertiary text-sm font-mono">Loading...</div>
        ) : (
          <div
            className={`shiki-viewer text-xs ${showLineNumbers ? 'with-line-numbers' : ''}`}
            dangerouslySetInnerHTML={{ __html: html }}
          />
        )}
      </div>

      <style>{`
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
          color: #484f58;
          user-select: none;
        }
        .shiki-viewer .line:hover {
          background: rgba(99, 110, 123, 0.1);
        }
      `}</style>
    </div>
  )
}
