import { type ReactNode, useCallback, useState } from 'react'
import { MiddleEllipsis } from './MiddleEllipsis'
import { Tooltip } from './Tooltip'
import { parseContextName } from '../../utils/context-name'
import type { ParsedContextName } from '../../utils/context-name'
import { assetUrl } from '../../utils/asset-url'
import awsLogoAsset from './provider-logos/aws.png'
import awsLogoDarkAsset from './provider-logos/aws-dark.png'
import gcpLogoAsset from './provider-logos/gcp.png'
import azureLogoAsset from './provider-logos/azure.svg'

// assetUrl normalizes the bundler-specific asset-import type (string under Vite,
// StaticImageData under webpack/Next) to a URL string usable in `<img src>`.
const awsLogo = assetUrl(awsLogoAsset)
const awsLogoDark = assetUrl(awsLogoDarkAsset)
const gcpLogo = assetUrl(gcpLogoAsset)
const azureLogo = assetUrl(azureLogoAsset)

// ClusterName renders a kubectl context string with the meaningful
// cluster identity surfaced as primary text and provider/region pushed
// into supporting metadata. Wraps parseContextName from utils/context-name
// so all surfaces (cluster cards, table cells, column headers, switcher
// trigger + dropdowns, breadcrumb, error views) share identical
// cluster-identity rendering.
//
// Width-aware: the name middle-truncates to fit its container so long
// strings (`gke_proj_us-east1-b_prod-cluster-us-east1`, custom user names)
// keep both ends readable. Tooltip surfaces the raw whenever EITHER the
// parse collapsed something (`gke_…` → `prod-cluster-us-east1`) OR the
// rendered name is being middle-truncated to fit.
//
// Variants:
//   inline   — name + small provider logo, fits in a table cell or
//              column header
//   stacked  — name on top, provider/region on a smaller second line,
//              for card-sized surfaces
//
// User-named clusters that don't match a known shape pass through
// unchanged. A `fallbackBadge` prop lets surfaces that always want a
// leading visual (e.g. the cluster-switcher trigger) supply one for the
// no-provider case.

type Provider = NonNullable<ParsedContextName['provider']>

// AWS uses the official aws+smile mark, which has dark navy text — needs
// a white-text variant on dark backgrounds. GCP (4-color cloud) and
// Azure (blue prism A) read fine on either theme.
const PROVIDER_LOGOS: Record<Provider, { light: string; dark?: string }> = {
  GKE: { light: gcpLogo },
  EKS: { light: awsLogo, dark: awsLogoDark },
  AKS: { light: azureLogo },
}

interface Props {
  /** Raw context / display string, as stored in the cluster record. */
  name: string
  /** Visual shape. Default: inline. */
  variant?: 'inline' | 'stacked'
  /** Suppress the provider badge — use when context already conveys provider.
   *  Also suppresses `fallbackBadge`; `noBadge` wins when both are set. */
  noBadge?: boolean
  /** Rendered in the badge slot when no provider is detected and `noBadge`
   *  is not set. Lets the cluster switcher trigger keep a Server-icon
   *  fallback for custom kubeconfig names without forcing every consumer
   *  to ship one. Ignored when `noBadge` is set. */
  fallbackBadge?: ReactNode
  /** Optional className on the outer span. */
  className?: string
  /** Suppress the hover tooltip even when the parsed name was collapsed
   *  or middle-truncated. Use when the surrounding chrome already
   *  discloses the raw context (e.g. inside an open switcher dropdown
   *  where the tooltip would overlap the popover content). */
  noTooltip?: boolean
}

function ProviderBadge({ provider }: { provider: Provider }) {
  const logos = PROVIDER_LOGOS[provider]
  // object-contain keeps the AWS aws+smile mark from being warped when
  // forced into a square box. GCP and Azure are square already.
  const baseClass = 'h-4 w-4 flex-shrink-0 object-contain'
  if (!logos.dark) {
    return <img src={logos.light} alt={`${provider} cluster`} className={baseClass} />
  }
  return (
    <>
      <img src={logos.light} alt={`${provider} cluster`} className={`${baseClass} dark:hidden`} />
      <img src={logos.dark} alt={`${provider} cluster`} className={`${baseClass} hidden dark:block`} />
    </>
  )
}

export function ClusterName({ name, variant = 'inline', noBadge, fallbackBadge, className, noTooltip }: Props) {
  const parsed = parseContextName(name)
  const [truncated, setTruncated] = useState(false)
  const onTruncatedChange = useCallback((t: boolean) => setTruncated(t), [])

  const hasProvider = parsed.provider !== null
  const showProviderBadge = !noBadge && hasProvider
  const showFallback = !noBadge && !hasProvider && fallbackBadge != null
  const showRegion = parsed.region !== null && variant === 'stacked'
  const collapsed = parsed.raw !== parsed.clusterName
  // The styled <Tooltip> wrapper is gated ONLY on `collapsed` — a stable,
  // string-derived fact. Truncation must NOT gate the wrapper: wrapping
  // changes the layout box MiddleEllipsis measures, so a truncated↔untruncated
  // flip oscillates through its ResizeObserver (the exact feedback its
  // onTruncatedChange note warns against). For the truncation case we disclose
  // the full name via MiddleEllipsis's native `title` instead — an attribute,
  // not a layout change — so the measurement can't feed back.
  const showRawTooltip = !noTooltip && collapsed
  // Don't also set a native title when the raw is already shown via the
  // wrapper (collapsed) — that would double up. Only the non-collapsed
  // truncation path uses it; there raw === clusterName.
  const truncationTitle = !noTooltip && !collapsed && truncated ? parsed.clusterName : undefined

  const body = (
    <span className={['inline-flex items-center gap-1.5 min-w-0', className ?? ''].join(' ')}>
      {showProviderBadge && <ProviderBadge provider={parsed.provider!} />}
      {showFallback && fallbackBadge}
      {variant === 'stacked' ? (
        <span className="flex flex-col min-w-0 flex-1">
          <MiddleEllipsis text={parsed.clusterName} title={truncationTitle} onTruncatedChange={onTruncatedChange} />
          {showRegion && (
            <span className="text-[10px] text-theme-text-tertiary truncate">
              {parsed.provider} · {parsed.region}
            </span>
          )}
        </span>
      ) : (
        <MiddleEllipsis text={parsed.clusterName} title={truncationTitle} onTruncatedChange={onTruncatedChange} />
      )}
    </span>
  )

  if (!showRawTooltip) return body

  return (
    <Tooltip content={parsed.raw} delay={250}>
      {body}
    </Tooltip>
  )
}
