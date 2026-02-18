import { useState, useRef, useCallback, KeyboardEvent } from 'react'
import { Send, Square } from 'lucide-react'
import { clsx } from 'clsx'

interface ChatInputProps {
  onSend: (message: string) => void
  onStop: () => void
  isLoading: boolean
  disabled?: boolean
  placeholder?: string
}

export function ChatInput({ onSend, onStop, isLoading, disabled, placeholder }: ChatInputProps) {
  const [input, setInput] = useState('')
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const handleSend = useCallback(() => {
    const trimmed = input.trim()
    if (!trimmed || isLoading) return
    onSend(trimmed)
    setInput('')
    // Reset textarea height
    if (textareaRef.current) {
      textareaRef.current.style.height = 'auto'
    }
  }, [input, isLoading, onSend])

  const handleKeyDown = useCallback((e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSend()
    }
  }, [handleSend])

  const handleInput = useCallback((e: React.ChangeEvent<HTMLTextAreaElement>) => {
    setInput(e.target.value)
    // Auto-resize textarea
    const el = e.target
    el.style.height = 'auto'
    el.style.height = Math.min(el.scrollHeight, 120) + 'px'
  }, [])

  return (
    <div className="border-t border-slate-700 bg-slate-800 p-3">
      <div className="flex items-end gap-2">
        <textarea
          ref={textareaRef}
          value={input}
          onChange={handleInput}
          onKeyDown={handleKeyDown}
          placeholder={placeholder || 'Ask about your cluster... (Enter to send, Shift+Enter for new line)'}
          disabled={disabled || isLoading}
          rows={1}
          className={clsx(
            'flex-1 bg-slate-900 border border-slate-600 rounded-lg px-3 py-2',
            'text-sm text-slate-200 placeholder-slate-500',
            'resize-none outline-none',
            'focus:border-blue-500 focus:ring-1 focus:ring-blue-500/30',
            'disabled:opacity-50 disabled:cursor-not-allowed',
            'scrollbar-thin scrollbar-thumb-slate-600'
          )}
          style={{ maxHeight: 120 }}
        />
        {isLoading ? (
          <button
            onClick={onStop}
            className="flex-shrink-0 p-2 rounded-lg bg-red-600 hover:bg-red-500 text-white transition-colors"
            title="Stop generating"
          >
            <Square className="w-4 h-4" />
          </button>
        ) : (
          <button
            onClick={handleSend}
            disabled={!input.trim() || disabled}
            className={clsx(
              'flex-shrink-0 p-2 rounded-lg transition-colors',
              input.trim() && !disabled
                ? 'bg-blue-600 hover:bg-blue-500 text-white'
                : 'bg-slate-700 text-slate-500 cursor-not-allowed'
            )}
            title="Send message"
          >
            <Send className="w-4 h-4" />
          </button>
        )}
      </div>
    </div>
  )
}
