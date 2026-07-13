"use client";

export function PageHeader({ title, description, actions }: { title: string; description: string; actions?: React.ReactNode }) {
  return (
    <header className="flex flex-col justify-between gap-4 px-4 py-5 sm:flex-row sm:items-center sm:px-6 lg:px-8 lg:py-6">
      <div className="min-w-0">
        <h1 className="text-heading-24 font-semibold text-fg">{title}</h1>
        <p className="mt-1 max-w-3xl text-copy-13 text-fg-muted">{description}</p>
      </div>
      {actions ? <div className="flex max-w-full shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
    </header>
  );
}
