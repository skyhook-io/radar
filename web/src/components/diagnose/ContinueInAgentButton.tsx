import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type FocusEvent,
  type KeyboardEvent,
} from "react";
import { Check, ChevronDown, Copy, TerminalSquare } from "lucide-react";
import { clsx } from "clsx";
import { copyText } from "@skyhook-io/k8s-ui/utils/clipboard";
import { Tooltip } from "../ui/Tooltip";
import { useAnimatedUnmount } from "../../hooks/useAnimatedUnmount";
import {
  TRANSITION_MENU,
  overlayExitMs,
  overlayTransitionStyle,
} from "../../utils/animation";
import { type RunSummary } from "../../api/diagnose";
import { mcpEndpointUrl } from "../../api/config";
import { useCapabilitiesContext } from "../../contexts/CapabilitiesContext";
import { buildLaunchCommand, launchAgentLabel, openInTerminal } from "./launch";

const TERMINAL_TITLE = "Radar Investigation";

export function useContinueInAgentCommand(run: RunSummary): string | null {
  const { mcpReadOnly } = useCapabilitiesContext();
  return buildLaunchCommand(run, mcpEndpointUrl(mcpReadOnly));
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
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [open]);

  useEffect(() => {
    if (open && isOpen) {
      menuRef.current
        ?.querySelector<HTMLElement>('[role="menuitem"]')
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
    if (event.key === "Tab") {
      setOpen(false);
      return;
    }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [],
    );
    if (items.length === 0) return;
    event.preventDefault();
    const at = items.indexOf(document.activeElement as HTMLElement);
    const step = event.key === "ArrowDown" ? 1 : -1;
    items[(at + step + items.length) % items.length].focus();
  };

  const segment =
    "inline-flex items-center gap-1.5 py-1 text-xs font-medium text-theme-text-primary hover:bg-theme-hover focus-visible:relative focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50";

  return (
    <div
      ref={rootRef}
      onBlur={onFocusLeave}
      className="relative inline-flex shrink-0"
    >
      <div className="inline-flex rounded-md border border-theme-border bg-theme-surface">
        {localTerminal ? (
          <>
            <Tooltip
              content={`Resume this session in ${label} with Radar's MCP, in Radar's terminal`}
              position="bottom"
              wrapperClassName="inline-flex"
            >
              <button
                type="button"
                onClick={runInTerminal}
                className={clsx(segment, "rounded-l-md px-2")}
              >
                <TerminalSquare
                  className="h-3.5 w-3.5 text-theme-text-tertiary"
                  aria-hidden
                />
                Continue in {label}
              </button>
            </Tooltip>
            <button
              ref={triggerRef}
              type="button"
              onClick={toggle}
              aria-haspopup="menu"
              aria-expanded={open}
              aria-controls={shouldRender ? menuId : undefined}
              aria-label="More ways to continue"
              className={clsx(
                segment,
                "rounded-r-md border-l border-theme-border px-1",
              )}
            >
              <ChevronDown
                className="h-3.5 w-3.5 text-theme-text-tertiary"
                aria-hidden
              />
            </button>
          </>
        ) : (
          <button
            ref={triggerRef}
            type="button"
            onClick={toggle}
            aria-haspopup="menu"
            aria-expanded={open}
            aria-controls={shouldRender ? menuId : undefined}
            className={clsx(segment, "rounded-md px-2")}
          >
            <TerminalSquare
              className="h-3.5 w-3.5 text-theme-text-tertiary"
              aria-hidden
            />
            Continue in {label}
            <ChevronDown
              className="h-3.5 w-3.5 text-theme-text-tertiary"
              aria-hidden
            />
          </button>
        )}
      </div>
      {shouldRender && (
        <div
          ref={menuRef}
          id={menuId}
          role="menu"
          aria-label={`Continue in ${label}`}
          inert={!open}
          onKeyDown={onMenuKeyDown}
          className={clsx(
            "absolute right-0 z-30 w-[min(22rem,calc(100cqw-2rem))] rounded-lg border border-theme-border bg-theme-surface p-1 shadow-theme-lg",
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
          <div className="px-2 pb-1.5 pt-1">
            <p className="text-[11px] text-theme-text-tertiary">
              Paste where you run {label}. It resumes this session with
              Radar&apos;s MCP attached.
            </p>
            <div className="mt-1.5 flex items-start gap-1 rounded-md border border-theme-border bg-theme-base py-1 pl-2 pr-1">
              <code
                data-continue-command
                className="max-h-24 min-w-0 flex-1 overflow-y-auto whitespace-pre-wrap break-all py-0.5 font-mono text-[11px] leading-snug text-theme-text-secondary"
              >
                {command}
              </code>
              <button
                type="button"
                role="menuitem"
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
                    <Check
                      className="h-3.5 w-3.5 text-emerald-500"
                      aria-hidden
                    />
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
          {localTerminal && (
            <>
              <div className="my-1 border-t border-theme-border" />
              <button
                type="button"
                role="menuitem"
                onClick={runInTerminal}
                className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs text-theme-text-primary hover:bg-theme-hover focus-visible:outline-none focus-visible:bg-theme-hover"
              >
                <TerminalSquare
                  className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary"
                  aria-hidden
                />
                Run in Radar terminal
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
