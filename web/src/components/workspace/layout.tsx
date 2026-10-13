import type { ComponentType, ReactNode } from 'react'
import { AlertTriangle } from 'lucide-react'

export function Notice({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-theme-border bg-theme-surface px-3 py-2 text-sm text-theme-text-secondary">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
      <div>{children}</div>
    </div>
  );
}

export function ScreenEmptyState({
  icon: Icon,
  title,
  detail,
  action,
}: {
  icon: ComponentType<{ className?: string }>;
  title: string;
  detail: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="flex min-h-0 flex-1 items-center justify-center bg-theme-base p-6">
      <div className="max-w-md text-center">
        <Icon className="mx-auto h-9 w-9 text-theme-text-tertiary/50" />
        <h2 className="mt-3 text-lg font-medium text-theme-text-primary">
          {title}
        </h2>
        <p className="mt-1 text-sm text-theme-text-secondary">{detail}</p>
        {action}
      </div>
    </div>
  );
}
