"use client";

import * as TabsPrimitive from "@radix-ui/react-tabs";
import { cn } from "@/lib/cn";

export const Tabs = TabsPrimitive.Root;

export function TabsList({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn("inline-flex min-h-9 max-w-full items-center gap-0.5 rounded-[7px] border border-border bg-subtle p-0.5", className)}
      {...props}
    />
  );
}

export function TabsTrigger({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      className={cn("control-text-sm inline-flex h-8 min-w-0 items-center justify-center rounded-[5px] border border-transparent px-3 font-medium text-fg-muted outline-none transition-[background-color,border-color,color,box-shadow] hover:text-fg focus-visible:shadow-focus disabled:pointer-events-none disabled:opacity-40 data-[state=active]:border-border data-[state=active]:bg-surface data-[state=active]:text-fg data-[state=active]:shadow-control", className)}
      {...props}
    />
  );
}

export function TabsContent({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Content>) {
  return <TabsPrimitive.Content className={cn("outline-none focus-visible:shadow-focus", className)} {...props} />;
}
