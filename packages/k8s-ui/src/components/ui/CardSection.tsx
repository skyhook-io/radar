import { Fragment, type ComponentType, type ReactNode } from "react";

// =============================================================================
// CARD SECTION PRIMITIVES — the shared "Sectioned + icons" vocabulary used by
// the Issue card (IssuesView) and the Check card (ChecksView) so both read as
// one product: ONE eyebrow style, an icon gutter per section, and a single body
// contrast floor (body text is never lighter than the secondary token, so it
// stays legible on the card surface). Severity is the only status color; section
// icons carry quiet role hues (what's-wrong = amber, fix = emerald, neutral =
// grey).
// =============================================================================

// Section role → eyebrow + icon hue. Body text always uses the secondary token
// (the contrast floor), regardless of tone.
export type CardSectionTone = "neutral" | "warn" | "fix";

const SECTION_TONE_CLASS: Record<CardSectionTone, string> = {
  // Eyebrow grey — the default, for neutral sections (Why it matters, Raw error,
  // Affected resources, Context, Subject).
  neutral: "text-theme-text-tertiary",
  // What's wrong — amber, the diagnosis beat.
  warn: "text-amber-700 dark:text-amber-300",
  // Next step / How to fix — emerald, the action beat.
  fix: "text-emerald-700 dark:text-emerald-400",
};

// The single eyebrow style: 11px / 600 / uppercase, with an icon in a fixed
// gutter so every section's body aligns under the same left edge.
export function CardSection({
  icon: Icon,
  label,
  tone = "neutral",
  labelExtra,
  children,
}: {
  icon: ComponentType<{ className?: string }>;
  label: string;
  tone?: CardSectionTone;
  /** Optional trailing eyebrow text (e.g. "· retried 5×"), rendered muted. */
  labelExtra?: ReactNode;
  children: ReactNode;
}) {
  const toneClass = SECTION_TONE_CLASS[tone];
  return (
    <section className="flex items-start gap-2.5">
      {/* Icon sits in a fixed gutter, anchored to the eyebrow line (top-aligned
          with a nudge to its optical center) — sections like Affected resources
          can be dozens of rows tall, and a section-centered glyph would float
          detached mid-list. */}
      <Icon
        className={`mt-[-1px] h-[18px] w-[18px] shrink-0 ${toneClass}`}
        aria-hidden
      />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <h4
          className={`text-[11px] font-semibold uppercase tracking-[0.06em] ${toneClass}`}
        >
          {label}
          {labelExtra ? (
            <span className="font-medium normal-case tracking-normal text-theme-text-tertiary">
              {" "}
              {labelExtra}
            </span>
          ) : null}
        </h4>
        {children}
      </div>
    </section>
  );
}

// Body paragraph at the contrast floor — 14px, secondary token, never lighter.
export function CardBody({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <p
      className={`text-sm leading-relaxed text-theme-text-secondary ${className ?? ""}`}
    >
      {children}
    </p>
  );
}

// The dark terminal block for raw error output. Intentionally dark in BOTH
// themes (it's a console surface, not chrome), so it draws from the dedicated
// --terminal-* tokens rather than the theme surface tokens.
export function TerminalBlockLabel({
  children,
  divider,
}: {
  children: ReactNode;
  divider?: boolean;
}) {
  return (
    <div
      className={`${divider ? "border-t" : "border-b"} border-[var(--terminal-divider)] px-3 py-1.5 text-[10px] font-semibold uppercase tracking-[0.06em] text-[var(--terminal-label)]`}
    >
      {children}
    </div>
  );
}

export function TerminalBlock({
  label,
  children,
  footer,
}: {
  label?: ReactNode;
  children: ReactNode;
  /** Rendered under the output, inside the same block: a folded tail, a divider. */
  footer?: ReactNode;
}) {
  return (
    <div className="overflow-hidden rounded-lg border border-[var(--terminal-border)] bg-[var(--terminal-bg)]">
      {label ? <TerminalBlockLabel>{label}</TerminalBlockLabel> : null}
      <pre className="overflow-x-auto whitespace-pre-wrap break-words px-3 py-2.5 font-mono text-xs leading-relaxed text-[var(--terminal-text)]">
        {children}
      </pre>
      {footer}
    </div>
  );
}

// The neutral category chip — drops the per-category hue so severity is the only
// status color. Self-contained (don't pair with badge-sm): 11px / 600, 2px×8px
// padding, no border, neutral grey.
export const NEUTRAL_CHIP_CLASS =
  "inline-flex items-center rounded-md px-2 py-0.5 text-[11px] font-semibold leading-tight bg-theme-elevated text-theme-text-secondary";

// Mono kind chip — the grey tag for an all-caps resource Kind (the Issue row
// header + the Subject footer row). Mono and tighter than NEUTRAL_CHIP_CLASS,
// which carries the label/category chips.
export const KIND_CHIP_CLASS =
  "shrink-0 rounded bg-theme-elevated px-1.5 py-px font-mono text-[11px] uppercase text-theme-text-secondary";

// Render a prose string, turning markdown `inline-code` spans into mono chips
// via the shared .inline-code class. Plain-text passthrough when the string
// carries no backticks, so it's a no-op for today's plain catalog copy and
// lights up automatically once backtick markup is added.
export function renderProse(text: string | undefined | null): ReactNode {
  if (!text) return null;
  if (!text.includes("`")) return text;
  const parts = text.split(/(`[^`]+`)/g);
  return parts.map((part, i) => {
    // length > 2 excludes a bare "``" (which the split leaves in a plain part)
    // so it renders as literal text, not an empty chip.
    if (part.length > 2 && part.startsWith("`") && part.endsWith("`")) {
      return (
        <code key={i} className="inline-code">
          {part.slice(1, -1)}
        </code>
      );
    }
    return <Fragment key={i}>{part}</Fragment>;
  });
}
