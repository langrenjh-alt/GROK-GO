"use client";

import { forwardRef } from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { LoaderCircle } from "lucide-react";
import { cn } from "@/lib/cn";

const buttonVariants = cva(
  "inline-flex max-w-full shrink-0 select-none items-center justify-center gap-2 overflow-hidden whitespace-nowrap rounded-[6px] border font-medium outline-none transition-[background-color,border-color,color,box-shadow] duration-150 focus-visible:shadow-focus disabled:pointer-events-none disabled:opacity-50 [&>svg]:shrink-0",
  {
    variants: {
      variant: {
        primary: "button-primary border-fg bg-fg shadow-control hover:border-fg-muted hover:bg-fg-muted",
        secondary: "border-border bg-surface text-fg shadow-control hover:border-border-strong hover:bg-subtle",
        tertiary: "border-transparent bg-transparent text-fg-muted hover:bg-subtle hover:text-fg",
        danger: "border-danger-strong bg-danger-strong text-white shadow-control hover:border-danger-strong-hover hover:bg-danger-strong-hover",
      },
      size: {
        small: "control-text-sm h-8 px-2.5",
        medium: "control-text h-9 px-3.5",
        large: "control-text h-10 px-4",
      },
    },
    defaultVariants: { variant: "primary", size: "medium" },
  },
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  loading?: boolean;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, loading, children, disabled, ...props }, ref) => (
    <button
      ref={ref}
      className={cn(buttonVariants({ variant, size }), className)}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...props}
    >
      {loading ? <LoaderCircle aria-hidden="true" className="size-4 animate-spin motion-reduce:animate-none" /> : null}
      {children}
    </button>
  ),
);
Button.displayName = "Button";

interface IconButtonProps extends Omit<ButtonProps, "children" | "aria-label"> {
  label: string;
  children: React.ReactNode;
}

export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(
  ({ label, children, size = "small", className, loading, ...props }, ref) => (
    <Button
      ref={ref}
      aria-label={label}
      title={label}
      size={size}
      className={cn("aspect-square px-0", className)}
      loading={loading}
      {...props}
    >
      {loading ? null : children}
    </Button>
  ),
);
IconButton.displayName = "IconButton";

export { buttonVariants };
