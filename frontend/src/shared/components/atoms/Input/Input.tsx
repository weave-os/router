import { cn } from "@/shared/tools/cn";

import * as React from "react";

export interface InputProps extends React.InputHTMLAttributes<HTMLInputElement> {}

/**
 * Renders a styled input field.
 */
export const Input = React.forwardRef<HTMLInputElement, InputProps>(
  ({ className, type, ...props }, ref) => {
    return (
      <input
        type={type}
        className={cn(
          "flex h-10 w-full rounded-md border border-wv-default bg-wv-inset px-3 py-2 text-body text-wv-primary ring-offset-wv-surface file:border-0 file:bg-[transparent] file:text-body file:font-medium placeholder:text-wv-placeholder hover:border-wv-hover focus-visible:border-wv-focus focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-wv-focus focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
        ref={ref}
        {...props}
      />
    );
  },
);
Input.displayName = "Input";
