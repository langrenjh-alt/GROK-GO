"use client";

import * as ToastPrimitive from "@radix-ui/react-toast";
import { CheckCircle2, CircleAlert, X } from "lucide-react";
import { createContext, useCallback, useContext, useState } from "react";

type ToastTone = "success" | "error";
interface ToastItem { id: number; message: string; tone: ToastTone }
const ToastContext = createContext<{ toast: (message: string, tone?: ToastTone) => void } | null>(null);

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const toast = useCallback((message: string, tone: ToastTone = "success") => {
    const id = Date.now() + Math.random();
    setItems((current) => [...current, { id, message, tone }]);
  }, []);
  return (
    <ToastContext.Provider value={{ toast }}>
      <ToastPrimitive.Provider duration={4500} swipeDirection="right">
        {children}
        {items.map((item) => (
          <ToastPrimitive.Root key={item.id} onOpenChange={(open) => { if (!open) setItems((current) => current.filter((entry) => entry.id !== item.id)); }} className="grid min-h-12 grid-cols-[20px_minmax(0,1fr)_24px] items-center gap-2 rounded-lg border border-border bg-surface px-3 py-2.5 shadow-menu data-[state=closed]:animate-toast-out data-[state=open]:animate-toast-in motion-reduce:animate-none">
            {item.tone === "success" ? <CheckCircle2 className="size-4 text-green" /> : <CircleAlert className="size-4 text-danger" />}
            <ToastPrimitive.Description className="break-words text-copy-13 text-fg">{item.message}</ToastPrimitive.Description>
            <ToastPrimitive.Close aria-label="Close" className="grid size-6 place-items-center rounded-[5px] text-fg-muted outline-none hover:bg-subtle focus-visible:shadow-focus"><X className="size-4" /></ToastPrimitive.Close>
          </ToastPrimitive.Root>
        ))}
        <ToastPrimitive.Viewport className="fixed bottom-3 right-3 z-[80] grid w-[calc(100%-24px)] max-w-sm gap-2 outline-none sm:bottom-4 sm:right-4" />
      </ToastPrimitive.Provider>
    </ToastContext.Provider>
  );
}

export function useToast() {
  const value = useContext(ToastContext);
  if (!value) throw new Error("useToast must be used within ToastProvider");
  return value;
}
