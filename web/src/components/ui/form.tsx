"use client";

import { forwardRef, useId } from "react";
import * as SwitchPrimitive from "@radix-ui/react-switch";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/cn";

interface FieldShellProps {
  id: string;
  label?: string;
  description?: string;
  error?: string;
  required?: boolean;
  children: React.ReactNode;
}

function FieldShell({ id, label, description, error, required, children }: FieldShellProps) {
  return (
    <div className="grid min-w-0 gap-1.5">
      {label ? (
        <label htmlFor={id} className="text-label-13 font-medium text-fg">
          {label}{required ? <span className="ml-1 text-danger" aria-hidden="true">*</span> : null}
        </label>
      ) : null}
      {children}
      {description && !error ? <p id={`${id}-description`} className="text-copy-13 text-fg-muted">{description}</p> : null}
      {error ? <p id={`${id}-error`} role="alert" className="text-copy-13 text-danger">{error}</p> : null}
    </div>
  );
}

export interface InputProps extends Omit<React.InputHTMLAttributes<HTMLInputElement>, "prefix"> {
  label?: string;
  description?: string;
  error?: string;
  prefix?: React.ReactNode;
  suffix?: React.ReactNode;
}

export const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ id: providedId, label, description, error, prefix, suffix, className, required, ...props }, ref) => {
    const generatedId = useId();
    const id = providedId ?? generatedId;
    const describedBy = error ? `${id}-error` : description ? `${id}-description` : undefined;
    return (
      <FieldShell id={id} label={label} description={description} error={error} required={required}>
        <div className={cn("group flex h-9 min-w-0 items-center rounded-[6px] border bg-surface shadow-control transition-[border-color,box-shadow,background-color] hover:border-border-strong focus-within:border-border-strong focus-within:shadow-focus", error ? "border-danger" : "border-border", props.disabled && "bg-subtle opacity-60 hover:border-border") }>
          {prefix ? <span className="pl-2.5 text-fg-muted">{prefix}</span> : null}
          <input
            ref={ref}
            id={id}
            required={required}
            aria-invalid={Boolean(error) || undefined}
            aria-describedby={describedBy}
            className={cn("control-text h-full min-w-0 flex-1 bg-transparent px-2.5 text-fg outline-none placeholder:text-fg-subtle disabled:cursor-not-allowed file:mr-3 file:border-0 file:bg-transparent file:text-[13px] file:font-medium file:text-fg", prefix && "pl-2", suffix && "pr-2", className)}
            {...props}
          />
          {suffix ? <span className="pr-2.5 text-fg-muted">{suffix}</span> : null}
        </div>
      </FieldShell>
    );
  },
);
Input.displayName = "Input";

export interface SelectProps extends React.SelectHTMLAttributes<HTMLSelectElement> {
  label?: string;
  description?: string;
  error?: string;
  options: Array<{ label: string; value: string }>;
}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(
  ({ id: providedId, label, description, error, options, className, required, ...props }, ref) => {
    const generatedId = useId();
    const id = providedId ?? generatedId;
    return (
      <FieldShell id={id} label={label} description={description} error={error} required={required}>
        <div className="relative">
          <select
            ref={ref}
            id={id}
            required={required}
            aria-invalid={Boolean(error) || undefined}
            aria-describedby={error ? `${id}-error` : description ? `${id}-description` : undefined}
            className={cn("control-text h-9 w-full appearance-none rounded-[6px] border border-border bg-surface px-2.5 pr-8 text-fg shadow-control outline-none transition-[border-color,box-shadow,background-color] hover:border-border-strong focus:border-border-strong focus:shadow-focus disabled:bg-subtle disabled:opacity-60 disabled:hover:border-border", className)}
            {...props}
          >
            {options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
          </select>
          <ChevronDown aria-hidden="true" className="pointer-events-none absolute right-2.5 top-1/2 size-4 -translate-y-1/2 text-fg-muted" />
        </div>
      </FieldShell>
    );
  },
);
Select.displayName = "Select";

export function Textarea({ label, description, error, id: providedId, className, ...props }: React.TextareaHTMLAttributes<HTMLTextAreaElement> & { label?: string; description?: string; error?: string }) {
  const generatedId = useId();
  const id = providedId ?? generatedId;
  return (
    <FieldShell id={id} label={label} description={description} error={error} required={props.required}>
      <textarea id={id} aria-invalid={Boolean(error) || undefined} aria-describedby={error ? `${id}-error` : description ? `${id}-description` : undefined} className={cn("control-text min-h-24 w-full resize-y rounded-[6px] border border-border bg-surface px-2.5 py-2 text-fg shadow-control outline-none transition-[border-color,box-shadow] placeholder:text-fg-subtle hover:border-border-strong focus:border-border-strong focus:shadow-focus", className)} {...props} />
    </FieldShell>
  );
}

export function Switch({ checked, onCheckedChange, label, description, disabled }: { checked: boolean; onCheckedChange: (value: boolean) => void; label: string; description?: string; disabled?: boolean }) {
  return (
    <label className={cn("flex min-w-0 cursor-pointer items-start justify-between gap-4", disabled && "cursor-not-allowed opacity-60")}>
      <span className="grid gap-0.5">
        <span className="text-label-14 font-medium text-fg">{label}</span>
        {description ? <span className="text-copy-13 text-fg-muted">{description}</span> : null}
      </span>
      <SwitchPrimitive.Root checked={checked} onCheckedChange={onCheckedChange} disabled={disabled} className="relative mt-0.5 h-5 w-9 shrink-0 rounded-full border border-border-strong bg-gray-300 outline-none transition-[background-color,border-color] data-[state=checked]:border-blue-700 data-[state=checked]:bg-blue-700 focus-visible:shadow-focus">
        <SwitchPrimitive.Thumb className="block size-4 translate-x-px rounded-full bg-white shadow-sm transition-transform data-[state=checked]:translate-x-[17px]" />
      </SwitchPrimitive.Root>
    </label>
  );
}
