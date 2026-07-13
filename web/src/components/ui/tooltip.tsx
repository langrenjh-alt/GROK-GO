"use client";

import * as TooltipPrimitive from "@radix-ui/react-tooltip";

export function Tooltip({ content, children }: { content: string; children: React.ReactNode }) {
  return (
    <TooltipPrimitive.Root>
      <TooltipPrimitive.Trigger asChild>{children}</TooltipPrimitive.Trigger>
      <TooltipPrimitive.Portal>
        <TooltipPrimitive.Content sideOffset={7} className="z-[70] max-w-64 rounded-[5px] border border-fg bg-fg px-2 py-1 text-[12px] leading-4 text-bg shadow-tooltip data-[state=delayed-open]:animate-fade-in motion-reduce:animate-none">
          {content}
          <TooltipPrimitive.Arrow className="fill-fg" />
        </TooltipPrimitive.Content>
      </TooltipPrimitive.Portal>
    </TooltipPrimitive.Root>
  );
}
