import { AlertTriangle, Inbox, LoaderCircle } from "lucide-react";
import { Button } from "./button";

export function LoadingState({ label = "Loading" }: { label?: string }) {
  return (
    <div role="status" className="grid min-h-52 place-items-center border-y border-border bg-surface">
      <div className="flex items-center gap-2.5 text-copy-13 text-fg-muted"><LoaderCircle aria-hidden="true" className="size-4 animate-spin" />{label}</div>
    </div>
  );
}

export function EmptyState({ title, description, action }: { title: string; description: string; action?: React.ReactNode }) {
  return (
    <div className="grid min-h-52 place-items-center border-y border-border bg-surface px-6 py-10 text-center">
      <div className="grid max-w-sm justify-items-center gap-3">
        <div className="grid size-9 place-items-center rounded-[7px] border border-border bg-subtle text-fg-muted shadow-control"><Inbox className="size-4" /></div>
        <div><h3 className="text-heading-16 font-semibold text-fg">{title}</h3><p className="mt-1 text-copy-13 text-fg-muted">{description}</p></div>
        {action}
      </div>
    </div>
  );
}

export function ErrorState({ title, description, onRetry }: { title: string; description: string; onRetry?: () => void }) {
  return (
    <div role="alert" className="flex min-h-40 flex-col items-start justify-center gap-4 border-y border-red-soft bg-red-soft px-5 py-6 sm:flex-row sm:items-center sm:justify-between">
      <div className="flex min-w-0 gap-3">
        <AlertTriangle className="mt-0.5 size-5 shrink-0 text-danger" />
        <div><h3 className="text-heading-16 font-semibold text-fg">{title}</h3><p className="mt-1 break-words text-copy-13 text-fg-muted">{description}</p></div>
      </div>
      {onRetry ? <Button variant="secondary" size="small" onClick={onRetry}>Retry</Button> : null}
    </div>
  );
}
