import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type FocusEvent,
  type KeyboardEvent,
} from "react";
import { Check, Copy, TerminalSquare } from "lucide-react";
import { clsx } from "clsx";
import { copyText } from "@skyhook-io/k8s-ui/utils/clipboard";
import { useAnimatedUnmount } from "../../hooks/useAnimatedUnmount";
import {
  TRANSITION_MENU,
  overlayExitMs,
  overlayTransitionStyle,
} from "../../utils/animation";
import { type RunSummary } from "../../api/diagnose";
import { mcpEndpointUrl } from "../../api/config";
import { useCapabilitiesContext } from "../../contexts/CapabilitiesContext";
import {
  buildLaunchCommand,
  launchAgentLabel,
  launchTrustHint,
  openInTerminal,
} from "./launch";

const TERMINAL_TITLE = "Radar Investigation";

export function useContinueInAgentCommand(
  run: RunSummary | null,
): string | null {
  const { mcpReadOnly, localShell } = useCapabilitiesContext();
  return run
    ? buildLaunchCommand(run, mcpEndpointUrl(mcpReadOnly), localShell)
    : null;
}

export function ContinueInAgentButton({
  run,
  command,
}: {
  run: RunSummary;
  command: string;
}) {
  const { localTerminal } = useCapabilitiesContext();
  const label = launchAgentLabel(run);
  const trustHint = launchTrustHint(run);
  const [open, setOpen] = useState(false);
  const [copy, setCopy] = useState<"idle" | "copied" | "failed">("idle");
  const { shouldRender, isOpen } = useAnimatedUnmount(
    open,
    overlayExitMs("menu"),
  );
  const rootRef = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuId = useId();
  const introId = useId();
  const hintId = useId();
  const copyingRef = useRef(false);
  // The menu lives inside the Findings scroller, which clips it: open toward
  // the side with room, and cap the height when neither side has enough.
  const [placement, setPlacement] = useState<{
    up: boolean;
    maxHeight?: number;
  }>({ up: false });

  useLayoutEffect(() => {
    if (!open || !shouldRender) return;
    const trigger = rootRef.current?.getBoundingClientRect();
    const menuHeight = menuRef.current?.scrollHeight ?? 0;
    if (!trigger) return;
    const bounds = rootRef.current
      ?.closest("[data-investigation-findings-scroll]")
      ?.getBoundingClientRect() ?? { top: 0, bottom: window.innerHeight };
    const below = bounds.bottom - trigger.bottom - 8;
    const above = trigger.top - bounds.top - 8;
    const up = menuHeight > below && above > below;
    const room = up ? above : below;
    setPlacement({ up, maxHeight: menuHeight > room ? room : undefined });
  }, [open, shouldRender]);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    // Capture phase, consumed: the Escape that closes this menu must not also
    // close the investigation panel.
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
      triggerRef.current?.focus();
    };
    // Placement is measured on open, so moving the pane closes the menu.
    const close = () => setOpen(false);
    const onScroll = (event: Event) => {
      if (!menuRef.current?.contains(event.target as Node)) close();
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown, true);
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", close);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown, true);
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", close);
    };
  }, [open]);

  useEffect(() => {
    if (open && isOpen) {
      menuRef.current
        ?.querySelector<HTMLElement>("button")
        ?.focus({ preventScroll: true });
    }
  }, [open, isOpen]);

  useEffect(() => {
    if (copy === "idle") return;
    const t = window.setTimeout(() => setCopy("idle"), 1400);
    return () => window.clearTimeout(t);
  }, [copy]);

  const toggle = () => {
    setCopy("idle");
    setOpen((v) => !v);
  };
  const runInTerminal = () => {
    setOpen(false);
    openInTerminal(command, TERMINAL_TITLE);
  };
  const copyCommand = () => {
    // The clipboard fallback focuses a temporary textarea outside the menu.
    copyingRef.current = true;
    void copyText(command).then((ok) => {
      copyingRef.current = false;
      setCopy(ok ? "copied" : "failed");
    });
  };
  // Only focus that lands somewhere else closes the menu. Safari does not
  // focus a clicked button, so a click inside blurs to nothing; clicks outside
  // are the pointerdown listener's job.
  const onFocusLeave = (event: FocusEvent<HTMLDivElement>) => {
    const next = event.relatedTarget as Node | null;
    if (next && !copyingRef.current && !rootRef.current?.contains(next))
      setOpen(false);
  };
  const onMenuKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLElement>("button") ?? [],
    );
    if (items.length === 0) return;
    // Tab moves between the panel's buttons; leaving either end closes it
    // and goes back to the button that opened it.
    if (event.key === "Tab") {
      const edge = event.shiftKey ? items[0] : items[items.length - 1];
      if (document.activeElement !== edge) return;
      event.preventDefault();
      setOpen(false);
      triggerRef.current?.focus();
      return;
    }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    event.preventDefault();
    const at = items.indexOf(document.activeElement as HTMLElement);
    const step = event.key === "ArrowDown" ? 1 : -1;
    items[(at + step + items.length) % items.length].focus();
  };

  // One button that always opens the panel: the hand-off is rare and
  // deliberate, so the panel can say what happens (and warn about Claude
  // Code's trust prompt) before anything runs. Docked, the header is narrow
  // and the label keeps only where it goes.
  return (
    <div
      ref={rootRef}
      onBlur={onFocusLeave}
      className="relative inline-flex shrink-0"
    >
      <button
        ref={triggerRef}
        type="button"
        onClick={toggle}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={shouldRender ? menuId : undefined}
        aria-label={`Continue in ${label}`}
        className="inline-flex items-center gap-1.5 whitespace-nowrap rounded-md border border-theme-border bg-theme-surface px-2 py-1 text-xs font-medium text-theme-text-primary hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
      >
        <TerminalSquare
          className="h-3.5 w-3.5 text-theme-text-tertiary"
          aria-hidden
        />
        <span>
          <span className="hidden @min-[640px]/diagnose-surface:inline">
            Continue in{" "}
          </span>
          {label}…
        </span>
      </button>
      {shouldRender && (
        <div
          ref={menuRef}
          id={menuId}
          role="dialog"
          aria-label={`Continue in ${label}`}
          aria-describedby={trustHint ? `${introId} ${hintId}` : introId}
          inert={!open}
          onKeyDown={onMenuKeyDown}
          className={clsx(
            "absolute right-0 z-30 w-[min(22rem,calc(100cqw-2rem))] rounded-lg border border-theme-border bg-theme-surface p-3 shadow-theme-lg",
            placement.maxHeight !== undefined && "overflow-y-auto",
            placement.up
              ? "bottom-full mb-1 origin-bottom-right"
              : "top-full mt-1 origin-top-right",
            TRANSITION_MENU,
            isOpen
              ? "translate-y-0 scale-100 opacity-100"
              : placement.up
                ? "translate-y-1 scale-[0.97] opacity-0"
                : "-translate-y-1 scale-[0.97] opacity-0",
            !open && "pointer-events-none",
          )}
          style={{
            ...overlayTransitionStyle(isOpen, "menu"),
            maxHeight: placement.maxHeight,
          }}
        >
          <p id={introId} className="text-xs text-theme-text-secondary">
            Resumes this session in {label} with Radar&apos;s MCP attached.
          </p>
          {trustHint && (
            <p
              id={hintId}
              data-continue-trust-hint
              className="mt-1 text-xs text-theme-text-tertiary"
            >
              {trustHint}
            </p>
          )}
          {localTerminal && (
            <button
              type="button"
              onClick={runInTerminal}
              className="btn-brand mt-3 inline-flex w-full items-center justify-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
            >
              <TerminalSquare className="h-3.5 w-3.5" aria-hidden />
              Run in Radar terminal
            </button>
          )}
          <p className="mt-3 text-[11px] text-theme-text-tertiary">
            {localTerminal ? "Or paste it" : "Paste it"} where you run {label}:
          </p>
          <div className="mt-1 flex items-start gap-1 rounded-md border border-theme-border bg-theme-base py-1 pl-2 pr-1">
            <code
              data-continue-command
              className="max-h-24 min-w-0 flex-1 overflow-y-auto whitespace-pre-wrap break-all py-0.5 font-mono text-[11px] leading-snug text-theme-text-secondary"
            >
              {command}
            </code>
            <button
              type="button"
              onClick={copyCommand}
              aria-label={
                copy === "copied"
                  ? "Command copied"
                  : copy === "failed"
                    ? "Copy failed"
                    : "Copy command"
              }
              aria-live="polite"
              className={clsx(
                "inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-1 text-[11px] font-medium hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50",
                copy === "copied"
                  ? "text-theme-text-primary"
                  : copy === "failed"
                    ? "text-warning-text"
                    : "text-theme-text-tertiary hover:text-theme-text-primary",
              )}
            >
              {copy === "copied" ? (
                <>
                  <Check className="h-3.5 w-3.5 text-success" aria-hidden />
                  Copied
                </>
              ) : copy === "failed" ? (
                "Copy failed"
              ) : (
                <Copy className="h-3.5 w-3.5" aria-hidden />
              )}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
