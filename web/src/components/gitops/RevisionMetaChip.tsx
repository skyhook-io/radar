import { ShieldCheck, ShieldAlert } from 'lucide-react'
import type { ReactNode } from 'react'
import { useArgoRevisionMetadata } from '../../api/client'
import { Tooltip } from '../ui/Tooltip'

// Argo returns the author as "Name <email>"; show just the name.
function authorName(author?: string): string {
  if (!author) return ''
  const lt = author.indexOf('<')
  return (lt >= 0 ? author.slice(0, lt) : author).trim()
}

// signatureInfo is a raw GPG verification line. "Good signature" → verified;
// any other non-empty value → a check ran but didn't verify; empty → unsigned.
function signatureState(sig?: string): 'good' | 'bad' | null {
  if (!sig) return null
  return /good signature/i.test(sig) ? 'good' : 'bad'
}

// RevisionMetaChip hydrates the Argo status-strip revision with Git commit
// detail (author, subject, signature) fetched from the Argo CD API. Renders
// nothing until data arrives, so the bare SHA is never blocked; the whole thing
// is gated by the host on capabilities.revisionMetadataAvailable.
export function RevisionMetaChip({
  appNamespace,
  appName,
  revision,
}: {
  appNamespace: string
  appName: string
  revision: string
}): ReactNode {
  // The host only renders this chip when revision metadata is available, so no
  // extra enabled gate is needed — the hook's own appName/revision guard suffices.
  const { data } = useArgoRevisionMetadata(appNamespace, appName, revision)
  if (!data) return null

  const author = authorName(data.author)
  const sig = signatureState(data.signatureInfo)
  const subject = data.message?.split('\n')[0]?.trim()
  if (!author && !sig && !subject) return null

  return (
    <>
      {author && <span className="shrink-0 text-theme-text-secondary">· {author}</span>}
      {subject && (
        <Tooltip content={data.message ?? subject} delay={300} wrapperClassName="min-w-0">
          <span className="max-w-[32ch] truncate text-theme-text-tertiary">“{subject}”</span>
        </Tooltip>
      )}
      {sig === 'good' && (
        <Tooltip content={data.signatureInfo ?? 'Signed commit'} delay={300} wrapperClassName="inline-flex shrink-0">
          <ShieldCheck className="h-3 w-3 text-green-600 dark:text-green-400/80" />
        </Tooltip>
      )}
      {sig === 'bad' && (
        <Tooltip content={data.signatureInfo ?? 'Signature not verified'} delay={300} wrapperClassName="inline-flex shrink-0">
          <ShieldAlert className="h-3 w-3 text-amber-600 dark:text-amber-400/80" />
        </Tooltip>
      )}
    </>
  )
}
