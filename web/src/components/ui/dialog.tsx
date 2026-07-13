"use client";

import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { cn } from "@/lib/cn";
import { IconButton } from "./button";

export const Dialog = DialogPrimitive.Root;
export const DialogTrigger = DialogPrimitive.Trigger;
export const DialogClose = DialogPrimitive.Close;

export function DialogContent({ children, title, description, className }: { children: React.ReactNode; title: string; description?: string; className?: string }) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/40 backdrop-blur-[1px] data-[state=closed]:animate-fade-out data-[state=open]:animate-fade-in motion-reduce:animate-none" />
      <DialogPrimitive.Content className={cn("scrollbar fixed inset-x-3 top-4 z-50 max-h-[calc(100dvh-32px)] w-auto max-w-lg overflow-y-auto rounded-lg border border-border bg-surface shadow-modal outline-none data-[state=closed]:animate-dialog-out data-[state=open]:animate-dialog-in motion-reduce:animate-none sm:inset-x-auto sm:left-1/2 sm:top-1/2 sm:w-[calc(100%-24px)] sm:-translate-x-1/2 sm:-translate-y-1/2", className)}>
        <div className="sticky top-0 z-10 flex items-start justify-between gap-4 border-b border-border bg-surface px-5 py-4">
          <div className="grid gap-1">
            <DialogPrimitive.Title className="text-heading-16 font-semibold text-fg">{title}</DialogPrimitive.Title>
            {description ? <DialogPrimitive.Description className="text-copy-13 text-fg-muted">{description}</DialogPrimitive.Description> : null}
          </div>
          <DialogPrimitive.Close asChild>
            <IconButton label="Close" variant="tertiary" className="-mr-1 -mt-1"><X className="size-4" /></IconButton>
          </DialogPrimitive.Close>
        </div>
        {children}
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}
