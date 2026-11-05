"use client";

import React from "react";

import { cn } from "../../../tools/cn";

export interface KbdProps extends React.HTMLAttributes<HTMLElement> {
  /**
   * Inverted color scheme
   * @default false
   */
  inverted?: boolean;

  /**
   * Size of the keyboard shortcut element
   * - default: Standard size
   * - sm: Smaller size for compact contexts like tooltips
   */
  size?: "default" | "sm";
}

export const Kbd = React.forwardRef<HTMLSpanElement, KbdProps>(
  ({ className, inverted = false, size = "default", ...props }, ref) => (
    <span
      ref={ref}
      className={cn(
        "grid place-items-center rounded border border-shortcuts font-sans font-normal leading-none text-wv-secondary",
        size === "default" && "size-6 text-caption",
        size === "sm" && "size-5 text-chart",
        inverted && "border-shortcuts-foreground",
        className,
      )}
      {...props}
    />
  ),
);
Kbd.displayName = "Kbd";
