import { useLayoutEffect, useRef, useState, type CSSProperties } from 'react'
import { PILL_MAX_PX, type GraphModel, type GraphNode, type GraphEdge, type LaneBox } from './reachGraphModel'
import { markStyle, glyphStyle, markHelp, edgeHelp, edgeHelpIsRedundant, SEV_COLOR, MARK_LEGEND, MARK_CATEGORY_LABEL, type Mark, type MarkCategory } from './reachMarks'
import { Tooltip } from '../ui/Tooltip'

/** Finding severity -> the shared health tones. Findings describe the OBJECT;
 *  Marks describe what happened to a request. The two vocabularies stay apart. */
const FINDING_TONE = { critical: 'unhealthy', warning: 'degraded', info: 'info' } as const

/** Upper bound on scale-up. Past this the graph stops reading as a diagram and
 *  starts reading as zoomed-in text. */
const MAX_SCALE = 1.5
/** Lower bound on scale-down. The graph's smallest type is 8.5px by design, so
 *  every point of scale-down comes straight off legibility - at 0.82 it landed
 *  near 7px. Below this the canvas keeps its size and the column scrolls
 *  instead, which is the lesser evil. */
const MIN_SCALE = 0.96
/** Overflow below this is left to the scrollbar - see `clipped` in useFit. */
const CLIP_HINT_MIN_PX = 48

/**
 * Fits the fixed design canvas to whatever width the column actually has.
 *
 * The geometry is hand-laid and must stay proportional - reflowing node
 * positions per breakpoint would destroy the spatial mapping the view teaches.
 * Scaling the whole canvas keeps that mapping while still using the full
 * column, in both directions: narrow columns shrink it instead of showing a
 * scrollbar, wide ones grow it instead of stranding dead space.
 */
function useFit(canvasW: number, canvasH: number): [React.RefObject<HTMLDivElement | null>, number, number, boolean] {
  const ref = useRef<HTMLDivElement | null>(null)
  const [box, setBox] = useState({ w: 0, h: 0 })
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    // Subtract the inset the canvas is positioned at, so a scaled-up graph does
    // not run under the column's edges.
    const measure = () => setBox({ w: el.clientWidth - 16, h: el.clientHeight - 12 })
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  // Fit to whichever axis binds, so a tall pane grows the graph instead of
  // leaving the space empty.
  const raw = box.w > 0 && box.h > 0 ? Math.min(box.w / canvasW, box.h / canvasH) : 1
  const scale = Math.min(MAX_SCALE, Math.max(MIN_SCALE, raw))
  // A chain long enough to hit the floor no longer fits, and the node that runs
  // off the right is the DESTINATION - the one the whole diagram is about. The
  // pane scrolls, but a scrollbar alone does not read as "the path continues".
  //
  // Only worth saying when a meaningful amount is hidden: the fade itself
  // obscures content, so firing it for the last few pixels of a node's padding
  // would hide more than it reveals.
  const clipped = box.w > 0 && canvasW * scale - box.w > CLIP_HINT_MIN_PX
  return [ref, scale, box.h > 0 ? box.h / scale : canvasH, clipped]
}

/**
 * The laned canvas. Nodes are absolutely positioned HTML over an SVG edge layer
 * so they can carry real typography and pinned anomalies - an all-SVG graph
 * would force text metrics we cannot control across themes.
 *
 * The canvas is a fixed design size inside a scroll container rather than a
 * fluid one: the layout encodes reading order (origin left, delivery right) and
 * a responsive reflow would break the very spatial mapping it exists to teach.
 */
export function ReachabilityGraph({
  model,
  selected,
  hovered,
  onSelect,
  onAction,
}: {
  model: GraphModel
  selected?: string
  /** Node the reader is pointing at from OUTSIDE the graph (the entry-problem
   *  rows under the header). Answers "where is it?" before a click is spent. */
  hovered?: string
  onSelect: (id: string) => void
  onAction?: (a: NonNullable<GraphNode['action']>) => void
}) {
  const { canvas, laneControl, laneData } = model
  const [fitRef, scale, availH, clipped] = useFit(canvas.w, canvas.h)
  // A lane is "these nodes", not "this region" - stretching it to the pane left
  // a tall empty box with the path pinned to its top edge. The card fills the
  // height; the graph is optically centred within its column instead.
  void availH
  return (
    <div className="flex h-full min-w-0 flex-col bg-theme-base">
      {/* The path is wide and short, so the space is used by SCALING the graph
          up to whichever axis binds - not by stretching the lane, which just
          floats the content in an empty band. Whatever height is left over is
          split evenly by centring.

          No minHeight on the measured element: the ResizeObserver watches it,
          so sizing it from a scale derived from its own height was a feedback
          loop that ratcheted the graph up to the cap. flex-1 inside a
          full-height column already gives it a definite height.

          The scrollbar gutter is reserved for the same reason: a horizontal
          scrollbar appearing here would shrink clientHeight, change the scale,
          and oscillate. */}
      <div className="relative flex min-h-0 min-w-0 flex-1">
      <div
        ref={fitRef}
        // `safe center` on BOTH axes, not plain centring. Plain `center` in a
        // scroll container pushes the leading edge outside the padding box when
        // the content overflows, and no amount of scrolling brings it back -
        // which is why the origin capsule was clipped off the top of a wide
        // fan-out. `safe` falls back to start exactly when that would happen.
        className="flex min-h-0 min-w-0 flex-1 overflow-auto px-2 py-1.5 [align-items:safe_center] [justify-content:safe_center] [scrollbar-gutter:stable]"
      >
        <div className="relative shrink-0" style={{ width: canvas.w * scale, height: canvas.h * scale }}>
        <div
          className="absolute left-0 top-0"
          style={{ width: canvas.w, height: canvas.h, transform: `scale(${scale})`, transformOrigin: 'top left' }}
        >
        {laneControl && <Lane box={laneControl} />}
        {laneData && <Lane box={laneData} />}
        <div>
        <svg width={canvas.w} height={canvas.h} viewBox={`0 0 ${canvas.w} ${canvas.h}`} className="absolute inset-0" style={{ overflow: 'visible' }}>
          {model.brackets.map((b, i) => (
            <path key={i} d={b.d} fill="none" stroke="var(--border-default)" strokeWidth={1} strokeDasharray="3 3" />
          ))}
          {model.edges.map((e) => {
            const s = markStyle(e.mark)
            // A boundary CONTINUATION extends one observed break across its
            // span - same colour so the span reads as one failure, dashed and
            // lighter so it never claims a second observation.
            const continuation = e.boundary === 'continuation'
            return (
              <path
                key={e.id}
                d={e.d}
                fill="none"
                stroke={s.color}
                strokeWidth={selected === e.id ? s.strokeWidth + 1 : s.strokeWidth}
                strokeDasharray={continuation ? '6 5' : s.dash}
                strokeOpacity={continuation ? 0.6 : s.strokeOpacity}
                strokeLinecap="round"
                className={e.mark === 'running' ? 'reach-edge-testing' : undefined}
              />
            )
          })}
        </svg>
        {/* An edge with no words carries its evidence in its line style alone -
            drawing an empty capsule for it put a bare glyph on top of whichever
            real pill shared that point. */}
        {model.edges
          .filter((e) => !!e.label)
          .map((e) => (
            <EdgePill key={e.id} edge={e} />
          ))}
        {model.nodes.map((n) => (
          <Node key={n.id} node={n} selected={selected === n.id} hovered={hovered === n.id} onSelect={onSelect} onAction={onAction} />
        ))}
        </div>
        </div>
        </div>
      </div>
      {clipped && (
        <>
          <div
            aria-hidden
            className="pointer-events-none absolute inset-y-0 right-0 w-10"
            // Not `transparent`: it interpolates through transparent BLACK, which
            // smears grey across a light background. Fade the surface to itself.
            style={{
              background:
                'linear-gradient(to right, color-mix(in srgb, var(--bg-base) 0%, transparent), var(--bg-base))',
            }}
          />
          <div className="pointer-events-none absolute bottom-1.5 right-2 rounded-full border border-theme-border bg-theme-surface px-1.5 py-0.5 text-[9px] font-semibold text-theme-text-tertiary">
            scroll for the rest of the path →
          </div>
        </>
      )}
      </div>
      <Legend model={model} />
    </div>
  )
}

function Lane({ box }: { box: LaneBox }) {
  return (
    <>
      <div
        className="absolute rounded-[10px]"
        style={{
          left: box.x,
          top: box.y,
          width: box.w,
          height: box.h,
          border: box.dashed ? '1px dashed var(--border-default)' : '1px solid var(--border-subtle)',
          background: `color-mix(in srgb, ${box.color} 4%, transparent)`,
          zIndex: 0,
        }}
      />
      {/* Sibling of the box, not a child: a lane that bounds one narrow node is
          far narrower than its label, and as a child the label was clipped by
          the box. */}
      <Tooltip
        content={box.help}
        wrapperClassName="absolute cursor-help"
        wrapperStyle={{ left: box.x + 12, top: box.y - 8, zIndex: 4 }}
      >
        <span
          className="whitespace-nowrap px-1.5 text-[9px] font-bold tracking-[0.06em] bg-theme-base"
          style={{ color: box.color }}
        >
          {box.label}
        </span>
      </Tooltip>
    </>
  )
}

/** Passive. There is no segment-local evidence behind an edge, so clicking one
 *  could only repeat the path result the sidebar already shows permanently. */
function EdgePill({ edge }: { edge: GraphEdge }) {
  const s = markStyle(edge.mark)
  return (
    <Tooltip
      content={
        <>
          <span className="font-semibold">{edge.title}</span>
          {!edgeHelpIsRedundant(edge.title, edge.label, edge.mark) && (
            <span className="text-theme-text-tertiary"> — {edgeHelp(edge.label, edge.mark)}</span>
          )}
        </>
      }
      wrapperClassName="absolute cursor-help"
      wrapperStyle={{ left: edge.px, top: edge.py, transform: 'translate(-50%,-50%)', zIndex: 3, maxWidth: PILL_MAX_PX }}
    >
      {/* Two lines, not one: the pill is capped to fit inside a gutter, which
          leaves room for about ten characters per line. On one line that turned
          every phrase into its first word plus an ellipsis. */}
      <div
        className="flex max-w-full items-start gap-1 rounded-full border border-theme-border bg-theme-surface px-2 py-0.5 text-[10px] font-semibold leading-[1.25] text-theme-text-secondary"
        style={{ boxShadow: '0 1px 2px rgba(0,0,0,.05)' }}
      >
        <span className="mt-px" style={glyphStyle(edge.mark)}>
          {s.glyph}
        </span>
        <span className="line-clamp-2 min-w-0">{edge.label}</span>
      </div>
    </Tooltip>
  )
}

function Node({
  node,
  selected,
  hovered,
  onSelect,
  onAction,
}: {
  node: GraphNode
  selected: boolean
  hovered?: boolean
  onSelect: (id: string) => void
  onAction?: (a: NonNullable<GraphNode['action']>) => void
}) {
  const isOrigin = node.isOrigin
  // An origin capsule carries exactly one status row; its action shares that
  // line instead of adding a full-width row beneath it.
  const inlineAction = !!(isOrigin && node.action && node.anomalies?.length === 1)
  // A test running FROM this vantage: the edge already animates, but the
  // capsule it leaves from looked as inert as everything else on the board.
  const testing = !!node.anomalies?.some((a) => a.mark === 'running')
  const style: CSSProperties = {
    left: node.x,
    top: node.y,
    width: node.w,
    // Pin to the height the layout reserved, so a box can never grow past its
    // slot and collide with the row beneath it.
    minHeight: node.h,
    zIndex: isOrigin ? 3 : 2,
    background: isOrigin ? 'var(--bg-elevated)' : 'var(--bg-surface)',
    // The origin is a vantage point, not a Kubernetes object. The dashed
    // capsule is what keeps those two categories from reading as one kind.
    borderRadius: isOrigin ? 22 : 9,
    border: selected
      ? '1.5px solid var(--accent)'
      : isOrigin
        ? `1.5px dashed ${node.lane === 'control' ? 'var(--color-info)' : 'var(--accent)'}`
        : '1px solid var(--border-default)',
    boxShadow: selected ? '0 0 0 3px var(--accent-muted)' : hovered ? '0 0 0 3px var(--color-warning)' : isOrigin ? 'none' : '0 1px 2px rgba(0,0,0,.05)',
    opacity: node.dim ? 0.5 : 1,
  }
  return (
    <div
      role="button"
      tabIndex={0}
      data-testing={testing || undefined}
      // The visible text is kind + name in separate spans a screen reader joins
      // unpredictably; one explicit name says what this is and (for a vantage
      // capsule) what selecting it does.
      aria-label={isOrigin ? `Vantage: ${node.name} — show what was tested from here` : `${node.kind} ${node.name}`}
      aria-pressed={selected}
      onClick={() => onSelect(node.id)}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onSelect(node.id)
        }
      }}
      className={`absolute cursor-pointer px-2.5 py-1.5 text-left${testing ? ' reach-node-testing' : ''}`}
      style={style}
    >
      <div className="flex items-center gap-1.5">
        <span className="min-w-0 flex-1 truncate font-mono text-[8.5px] font-bold tracking-[0.06em] text-theme-text-tertiary">{node.kind}</span>
        {node.tag && <TinyTag text={node.tag} tone={node.lane === 'control' ? 'var(--color-info)' : 'var(--color-warning-dark)'} />}
        {!isOrigin && (
          <span
            className="inline-block shrink-0 rounded-full"
            style={{ width: 8, height: 8, background: SEV_COLOR[node.tone] }}
            aria-label={`health ${node.tone}`}
          />
        )}
      </div>
      <Tooltip content={node.name} wrapperClassName="block min-w-0">
        <div className="mt-0.5 truncate font-mono text-[11.5px] font-semibold text-theme-text-primary">{node.name}</div>
      </Tooltip>
      <div className="mt-px text-[10px] leading-[1.35] text-theme-text-tertiary">{node.sub}</div>
      {/* What is actually WRONG with this hop, on the hop. The backend already
          produces a parsed cause per finding; the graph previously spent it on a
          dot colour and made the reader click to learn the rest. Deliberately a
          headline only - the message, action and remediation stay in the
          inspector so this never becomes a place you read paragraphs. */}
      {node.notes && node.notes.length > 0 && (
        <div className="mt-1.5 flex flex-col gap-0.5 border-t border-theme-border-subtle pt-1.5">
          {node.notes.map((n, i) => (
            <Tooltip key={i} content={n.detail || n.text} wrapperClassName="w-full cursor-help">
              <div className="flex w-full items-start gap-1.5">
                <span
                  className="mt-1 inline-block shrink-0 rounded-full"
                  style={{ width: 6, height: 6, background: SEV_COLOR[FINDING_TONE[n.severity]] }}
                />
                {/* Hard guard: whatever the producer writes, a node note may
                    never grow past two lines and push the graph around. */}
                <span className="line-clamp-2 min-w-0 flex-1 text-left text-[9.5px] leading-[1.35] text-theme-text-secondary">
                  {n.text}
                </span>
              </div>
            </Tooltip>
          ))}
        </div>
      )}
      {node.anomalies && node.anomalies.length > 0 && (
        <div className="mt-1.5 flex flex-col gap-0.5 border-t border-theme-border-subtle pt-1.5">
          {node.anomalies.map((a, i) => (
            <div key={i} className={`flex gap-1.5 ${inlineAction && i === 0 ? 'items-center' : 'items-baseline'}`}>
              <MarkGlyph mark={a.mark} />
              {/* Rows truncate visually; the full sentence must always be a
                  hover away - a cut "reached, redirect…" hid its destination. */}
              <Tooltip content={a.title || a.text} wrapperClassName="min-w-0 flex-1">
                <span className={`block truncate text-[9.5px] leading-[1.35] text-theme-text-secondary${a.mark === 'running' ? ' reach-label-testing' : ''}`}>
                  {a.text}
                </span>
              </Tooltip>
              {/* The capsule's single status row and its action share the line -
                  a stacked full-width button grew the capsule past the height
                  the layout reserved, out the bottom of its lane box. */}
              {inlineAction && i === 0 && (
                <button
                  type="button"
                  disabled={!!node.action!.disabledReason}
                  title={node.action!.disabledReason || node.action!.text}
                  onClick={(e) => {
                    e.stopPropagation()
                    if (!node.action?.disabledReason) onAction?.(node.action!)
                  }}
                  className="shrink-0 whitespace-nowrap rounded border border-theme-border bg-theme-surface px-1.5 py-0.5 text-[9px] font-semibold text-theme-text-primary transition-colors hover:bg-theme-hover disabled:cursor-not-allowed disabled:opacity-50"
                >
                  ⚗ Run now
                </button>
              )}
            </div>
          ))}
        </div>
      )}
      {/* Per-endpoint delivery, as rows rather than a column of boxes - same
          truth, a fraction of the width, and more of them visible at once. */}
      {node.podRows && node.podRows.length > 0 && (
        <div className="mt-1.5 flex flex-col gap-0.5 border-t border-theme-border-subtle pt-1.5">
          {node.podRows.map((r) => (
            <Tooltip
              key={r.name}
              content={
                <>
                  <span className="font-mono font-semibold">{r.name}</span>
                  <span className="text-theme-text-tertiary"> — {r.detail} · {markHelp(r.mark)}</span>
                </>
              }
              wrapperClassName="w-full cursor-help"
            >
              <div className="flex w-full items-baseline gap-1.5">
                <span style={glyphStyle(r.mark)}>{markStyle(r.mark).glyph}</span>
                <span className="min-w-0 flex-[2] truncate font-mono text-[9.5px] text-theme-text-secondary">{r.name}</span>
                {/* Was shrink-0, so a long detail could not give way and ran
                    straight out of the node. Both sides truncate now; the full
                    text is on the hover either way. */}
                <span className="min-w-0 flex-1 truncate text-right text-[9px] text-theme-text-tertiary">{r.detail}</span>
              </div>
            </Tooltip>
          ))}
          {!!node.moreRows && <div className="text-[9px] text-theme-text-tertiary">+{node.moreRows} more not shown</div>}
        </div>
      )}
      {/* Offered where the gap is. Deliberately its own button rather than a
          click on the capsule: selecting a vantage is free, and this creates
          Pods in the user's cluster. Full-width form only when the status row
          could not host it inline. */}
      {node.action && !inlineAction && (
        <button
          type="button"
          disabled={!!node.action.disabledReason}
          title={node.action.disabledReason}
          onClick={(e) => {
            e.stopPropagation()
            if (!node.action?.disabledReason) onAction?.(node.action!)
          }}
          className="mt-1.5 w-full rounded border border-theme-border bg-theme-surface px-2 py-1 text-[9.5px] font-semibold text-theme-text-primary transition-colors hover:bg-theme-hover disabled:cursor-not-allowed disabled:opacity-50"
        >
          ⚗ {node.action.text}
        </button>
      )}
    </div>
  )
}

export function TinyTag({ text, tone, title }: { text: string; tone: string; title?: string }) {
  const tag = (
    <span
      className="whitespace-nowrap rounded-full px-1.5 py-px text-[8.5px] font-bold tracking-[0.05em]"
      style={{
        color: tone,
        border: `1px solid ${tone}`,
        // Opaque, not tinted-transparent: the origin capsule's dashed border ran
        // straight through the tag when this let the backdrop show.
        background: `color-mix(in srgb, ${tone} 10%, var(--bg-elevated))`,
      }}
    >
      {text}
    </span>
  )
  if (!title) return tag
  return (
    <Tooltip content={title} wrapperClassName="cursor-help">
      {tag}
    </Tooltip>
  )
}

/** A mark's symbol with its meaning on hover. The symbol set is the graph's
 *  whole evidence vocabulary and appears in six places; without one component
 *  each site had to remember to attach the decoder, and some did not. */
export function MarkGlyph({ mark }: { mark: Mark }) {
  return (
    <Tooltip content={markHelp(mark)} wrapperClassName="cursor-help">
      <span style={glyphStyle(mark)}>{markStyle(mark).glyph}</span>
    </Tooltip>
  )
}

/**
 * One line, not a symbol table.
 *
 * Every glyph on screen already sits beside its own words - an edge pill, a
 * capsule verdict, a Pod row - and every one carries markHelp on hover. Spelling
 * the vocabulary out again cost a permanent band to repeat what the thing itself
 * says. What is NOT discoverable anywhere is the GRAMMAR: that a dot and a line
 * answer different questions. That stays, with the vocabulary on hover for
 * anyone who wants the whole set.
 */
function Legend({ model }: { model: GraphModel }) {
  const present = new Set<Mark>([
    ...model.edges.map((e) => e.mark),
    ...model.nodes.flatMap((n) => [...(n.anomalies ?? []).map((a) => a.mark), ...(n.podRows ?? []).map((r) => r.mark)]),
  ])
  const shown = MARK_LEGEND.filter((l) => present.has(l.mark))
  if (shown.length === 0) return null
  // Grouped, not flat: a flat list teaches the vocabulary as one axis, and it
  // is not - what happened, how it was tested, and why nothing ran are
  // different questions.
  const categories = [...new Set(shown.map((l) => l.category))] as MarkCategory[]
  return (
    <div className="mt-0.5 flex items-center justify-end border-t border-theme-border-subtle px-3.5 py-1.5 text-[10.5px] text-theme-text-tertiary">
      <Tooltip
        content={
          <span className="flex flex-col gap-1">
            {categories.map((c) => (
              <span key={c} className="flex flex-col gap-0.5">
                <span className="text-[9px] font-bold uppercase tracking-[0.06em] opacity-70">{MARK_CATEGORY_LABEL[c]}</span>
                {shown
                  .filter((l) => l.category === c)
                  .map((l) => (
                    <span key={l.mark} className="inline-flex items-baseline gap-1.5">
                      <span style={glyphStyle(l.mark as Mark)}>{markStyle(l.mark as Mark).glyph}</span>
                      {l.text}
                    </span>
                  ))}
              </span>
            ))}
          </span>
        }
        wrapperClassName="cursor-help"
      >
        {/* The dot IS resource health (findings + readiness), never probe
            outcomes - it must not change when the selected vantage does. */}
        <span className="italic">dot = how this resource is doing · line = did traffic get through, from the selected vantage</span>
      </Tooltip>
    </div>
  )
}
