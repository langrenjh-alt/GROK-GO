import { cn } from "@/lib/cn";

const toneClasses = {
  neutral: "border-border bg-subtle text-fg-muted",
  blue: "border-blue-soft bg-blue-soft text-blue",
  green: "border-green-soft bg-green-soft text-green",
  amber: "border-amber-soft bg-amber-soft text-amber",
  red: "border-red-soft bg-red-soft text-danger",
} as const;

export function Badge({ children, tone = "neutral", dot = false, className }: { children: React.ReactNode; tone?: keyof typeof toneClasses; dot?: boolean; className?: string }) {
  return (
    <span className={cn("badge-text inline-flex h-[22px] max-w-full items-center gap-1.5 rounded-[5px] border px-1.5 font-medium", toneClasses[tone], className)}>
      {dot ? <span aria-hidden="true" className="size-1.5 shrink-0 rounded-full bg-current" /> : null}
      <span className="truncate">{children}</span>
    </span>
  );
}
