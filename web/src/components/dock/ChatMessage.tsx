import { memo, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { User, Bot, ChevronDown, ChevronRight, Wrench, AlertCircle } from 'lucide-react'
import { clsx } from 'clsx'

export interface ToolCallInfo {
  id: string
  name: string
  arguments: string
  result?: string
  isError?: boolean
}

interface ChatMessageProps {
  role: 'user' | 'assistant'
  content: string
  isStreaming?: boolean
  toolCalls?: ToolCallInfo[]
}

export const ChatMessage = memo(function ChatMessage({ role, content, isStreaming, toolCalls }: ChatMessageProps) {
  const isUser = role === 'user'

  return (
    <div className={clsx('flex gap-3 px-4 py-3', isUser ? 'bg-slate-800/50' : 'bg-slate-900')}>
      <div className={clsx(
        'flex-shrink-0 w-7 h-7 rounded-full flex items-center justify-center',
        isUser ? 'bg-blue-600' : 'bg-emerald-600'
      )}>
        {isUser ? <User className="w-4 h-4 text-white" /> : <Bot className="w-4 h-4 text-white" />}
      </div>
      <div className="flex-1 min-w-0 overflow-hidden">
        <div className="text-xs text-slate-400 mb-1 font-medium">
          {isUser ? 'You' : 'AI Assistant'}
        </div>

        {/* Tool calls section */}
        {toolCalls && toolCalls.length > 0 && (
          <div className="mb-2 space-y-1">
            {toolCalls.map(tc => (
              <ToolCallBlock key={tc.id} toolCall={tc} />
            ))}
          </div>
        )}

        {isUser ? (
          <div className="text-sm text-slate-200 whitespace-pre-wrap break-words">{content}</div>
        ) : (
          <div className="text-sm text-slate-200 prose prose-invert prose-sm max-w-none
            prose-p:my-1 prose-pre:my-2 prose-ul:my-1 prose-ol:my-1
            prose-headings:text-slate-100 prose-code:text-emerald-300
            prose-pre:bg-slate-800 prose-pre:border prose-pre:border-slate-700
            prose-a:text-blue-400 prose-strong:text-slate-100
            break-words overflow-hidden">
            <ReactMarkdown>{content || (isStreaming ? '...' : '')}</ReactMarkdown>
            {isStreaming && content && (
              <span className="inline-block w-2 h-4 bg-emerald-400 animate-pulse ml-0.5 align-text-bottom" />
            )}
          </div>
        )}
      </div>
    </div>
  )
})

function ToolCallBlock({ toolCall }: { toolCall: ToolCallInfo }) {
  const [expanded, setExpanded] = useState(false)

  const toolLabel = formatToolName(toolCall.name)
  let argsSummary = ''
  try {
    const args = JSON.parse(toolCall.arguments)
    const parts: string[] = []
    for (const [k, v] of Object.entries(args)) {
      if (v) parts.push(`${k}=${v}`)
    }
    argsSummary = parts.join(', ')
  } catch {
    argsSummary = toolCall.arguments
  }

  return (
    <div className="rounded border border-slate-700 bg-slate-800/50 text-xs overflow-hidden">
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-2.5 py-1.5 hover:bg-slate-700/50 transition-colors text-left"
      >
        {expanded
          ? <ChevronDown className="w-3 h-3 text-slate-500 flex-shrink-0" />
          : <ChevronRight className="w-3 h-3 text-slate-500 flex-shrink-0" />
        }
        <Wrench className="w-3 h-3 text-amber-400 flex-shrink-0" />
        <span className="text-amber-300 font-medium">{toolLabel}</span>
        {argsSummary && (
          <span className="text-slate-500 truncate">({argsSummary})</span>
        )}
        {toolCall.isError && (
          <AlertCircle className="w-3 h-3 text-red-400 flex-shrink-0 ml-auto" />
        )}
        {toolCall.result !== undefined && !toolCall.isError && (
          <span className="text-emerald-500 ml-auto flex-shrink-0">done</span>
        )}
        {toolCall.result === undefined && (
          <span className="text-amber-400 ml-auto flex-shrink-0 animate-pulse">running...</span>
        )}
      </button>

      {expanded && toolCall.result !== undefined && (
        <div className="px-2.5 py-2 border-t border-slate-700 max-h-60 overflow-auto">
          <pre className={clsx(
            'text-[11px] leading-relaxed whitespace-pre-wrap break-all',
            toolCall.isError ? 'text-red-300' : 'text-slate-300'
          )}>
            {formatToolResult(toolCall.result)}
          </pre>
        </div>
      )}
    </div>
  )
}

function formatToolName(name: string): string {
  return name.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase())
}

function formatToolResult(result: string): string {
  try {
    const parsed = JSON.parse(result)
    return JSON.stringify(parsed, null, 2)
  } catch {
    return result
  }
}
