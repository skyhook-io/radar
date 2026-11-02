import { ArrowRight } from "lucide-react";

export function ExampleInvestigationLink({
  onOpen,
  className = "",
}: {
  onOpen: () => void;
  className?: string;
}) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className={`inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline ${className}`}
    >
      See an example investigation
      <ArrowRight aria-hidden className="h-3 w-3" />
    </button>
  );
}
