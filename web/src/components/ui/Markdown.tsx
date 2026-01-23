import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { clsx } from 'clsx'

interface MarkdownProps {
  children: string
  className?: string
}

export function Markdown({ children, className }: MarkdownProps) {
  return (
    <div className={clsx('markdown-content', className)}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
        h1: ({ children }) => (
          <h1 className="text-lg font-bold text-theme-text-primary mt-4 mb-2 first:mt-0">{children}</h1>
        ),
        h2: ({ children }) => (
          <h2 className="text-base font-semibold text-theme-text-primary mt-4 mb-2 first:mt-0">{children}</h2>
        ),
        h3: ({ children }) => (
          <h3 className="text-sm font-semibold text-theme-text-primary mt-3 mb-1.5">{children}</h3>
        ),
        h4: ({ children }) => (
          <h4 className="text-sm font-medium text-theme-text-primary mt-2 mb-1">{children}</h4>
        ),
        p: ({ children }) => (
          <p className="text-theme-text-secondary my-2 leading-relaxed">{children}</p>
        ),
        a: ({ href, children }) => (
          <a
            href={href}
            target="_blank"
            rel="noopener noreferrer"
            className="text-blue-400 hover:text-blue-300 underline underline-offset-2"
          >
            {children}
          </a>
        ),
        ul: ({ children }) => (
          <ul className="list-disc list-inside my-2 space-y-1 text-theme-text-secondary">{children}</ul>
        ),
        ol: ({ children }) => (
          <ol className="list-decimal list-inside my-2 space-y-1 text-theme-text-secondary">{children}</ol>
        ),
        li: ({ children }) => (
          <li className="leading-relaxed">{children}</li>
        ),
        code: ({ className, children }) => {
          const isInline = !className
          if (isInline) {
            return (
              <code className="px-1.5 py-0.5 bg-theme-elevated rounded text-theme-text-primary font-mono text-[0.9em]">
                {children}
              </code>
            )
          }
          return (
            <code className="block font-mono text-theme-text-secondary">{children}</code>
          )
        },
        pre: ({ children }) => (
          <pre className="my-3 p-3 bg-theme-elevated rounded-lg overflow-x-auto text-xs font-mono text-theme-text-secondary">
            {children}
          </pre>
        ),
        blockquote: ({ children }) => (
          <blockquote className="border-l-2 border-theme-border pl-3 my-2 text-theme-text-tertiary italic">
            {children}
          </blockquote>
        ),
        table: ({ children }) => (
          <div className="my-3 overflow-x-auto">
            <table className="min-w-full border-collapse text-sm">{children}</table>
          </div>
        ),
        thead: ({ children }) => (
          <thead className="bg-theme-elevated">{children}</thead>
        ),
        tbody: ({ children }) => (
          <tbody className="divide-y divide-theme-border">{children}</tbody>
        ),
        tr: ({ children }) => (
          <tr className="border-b border-theme-border">{children}</tr>
        ),
        th: ({ children }) => (
          <th className="px-3 py-2 text-left text-theme-text-primary font-medium border border-theme-border">
            {children}
          </th>
        ),
        td: ({ children }) => (
          <td className="px-3 py-2 text-theme-text-secondary border border-theme-border">{children}</td>
        ),
        hr: () => <hr className="my-4 border-theme-border" />,
        strong: ({ children }) => (
          <strong className="font-semibold text-theme-text-primary">{children}</strong>
        ),
        em: ({ children }) => (
          <em className="italic">{children}</em>
        ),
      }}
      >
        {children}
      </ReactMarkdown>
    </div>
  )
}
