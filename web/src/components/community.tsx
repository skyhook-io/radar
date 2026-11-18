// A radarhq.io redirect rather than the Slack invite itself: invite links
// expire, and shipped binaries keep this URL for months.
export const COMMUNITY_SLACK_URL = "https://radarhq.io/slack";

export const COMMUNITY_HINT =
  "Join our Slack to ask questions, get help and share feedback with the Radar team";

// The mark stays one theme color and takes Slack's colors only on hover, so it
// doesn't outshout the rail. Paths read --slack-* and fall back to currentColor;
// these set them. Literal strings so Tailwind's scanner sees them.
export const SLACK_COLORS_ON_ROW_HOVER =
  "group-hover/item:[--slack-red:#E01E5A] group-hover/item:[--slack-blue:#36C5F0] group-hover/item:[--slack-green:#2EB67D] group-hover/item:[--slack-yellow:#ECB22E]";
export const SLACK_COLORS_ON_HOVER =
  "group-hover:[--slack-red:#E01E5A] group-hover:[--slack-blue:#36C5F0] group-hover:[--slack-green:#2EB67D] group-hover:[--slack-yellow:#ECB22E]";

// lucide-react no longer ships brand marks.
export function SlackIcon({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" aria-hidden>
      <path
        className="transition-colors"
        fill="var(--slack-red, currentColor)"
        d="M5.042 15.165a2.528 2.528 0 0 1-2.52 2.523A2.528 2.528 0 0 1 0 15.165a2.527 2.527 0 0 1 2.522-2.52h2.52v2.52zM6.313 15.165a2.527 2.527 0 0 1 2.521-2.52 2.527 2.527 0 0 1 2.521 2.52v6.313A2.528 2.528 0 0 1 8.834 24a2.528 2.528 0 0 1-2.521-2.522v-6.313z"
      />
      <path
        className="transition-colors"
        fill="var(--slack-blue, currentColor)"
        d="M8.834 5.042a2.528 2.528 0 0 1-2.521-2.52A2.528 2.528 0 0 1 8.834 0a2.528 2.528 0 0 1 2.521 2.522v2.52H8.834zM8.834 6.313a2.528 2.528 0 0 1 2.521 2.521 2.528 2.528 0 0 1-2.521 2.521H2.522A2.528 2.528 0 0 1 0 8.834a2.528 2.528 0 0 1 2.522-2.521h6.312z"
      />
      <path
        className="transition-colors"
        fill="var(--slack-green, currentColor)"
        d="M18.956 8.834a2.528 2.528 0 0 1 2.522-2.521A2.528 2.528 0 0 1 24 8.834a2.528 2.528 0 0 1-2.522 2.521h-2.522V8.834zM17.688 8.834a2.528 2.528 0 0 1-2.523 2.521 2.527 2.527 0 0 1-2.52-2.521V2.522A2.527 2.527 0 0 1 15.165 0a2.528 2.528 0 0 1 2.523 2.522v6.312z"
      />
      <path
        className="transition-colors"
        fill="var(--slack-yellow, currentColor)"
        d="M15.165 18.956a2.528 2.528 0 0 1 2.523 2.522A2.528 2.528 0 0 1 15.165 24a2.527 2.527 0 0 1-2.52-2.522v-2.522h2.52zM15.165 17.688a2.527 2.527 0 0 1-2.52-2.523 2.526 2.526 0 0 1 2.52-2.52h6.313A2.527 2.527 0 0 1 24 15.165a2.528 2.528 0 0 1-2.522 2.523h-6.313z"
      />
    </svg>
  );
}
