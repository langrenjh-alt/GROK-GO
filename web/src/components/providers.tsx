"use client";

import { ThemeProvider } from "next-themes";
import * as TooltipPrimitive from "@radix-ui/react-tooltip";
import { I18nProvider } from "./i18n-provider";
import { ToastProvider } from "./ui/toast";

export function Providers({ children }: { children: React.ReactNode }) {
  return (
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem disableTransitionOnChange>
      <I18nProvider>
        <TooltipPrimitive.Provider delayDuration={350} skipDelayDuration={150}>
          <ToastProvider>{children}</ToastProvider>
        </TooltipPrimitive.Provider>
      </I18nProvider>
    </ThemeProvider>
  );
}
