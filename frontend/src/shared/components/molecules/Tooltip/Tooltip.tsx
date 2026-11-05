import * as TooltipPrimitive from "@radix-ui/react-tooltip";

import React from "react";

import { cn } from "../../../tools/cn";

/**
 * Global provider to enable tooltip functionality. Should only be used once in the application.
 */
export const TooltipProvider = TooltipPrimitive.Provider;

export interface TooltipProps
  extends
    React.ComponentProps<typeof TooltipPrimitive.Root>,
    Pick<React.ComponentProps<typeof TooltipPrimitive.Content>, "align" | "side"> {
  /**
   * The content of the tooltip.
   */
  content: React.ReactNode;

  /**
   * Class name for the container holding the content. Useful for styling the popup.
   */
  contentClassName?: string;

  /**
   * Whether to show an arrow on the tooltip.
   *
   * @default false
   */
  arrow?: boolean;

  /**
   * Class name for the arrow element. Useful for styling the arrow. Only used if `arrow` is true.
   */
  arrowClassName?: string;

  /**
   * If true, the tooltip will not add a new interactive element to the DOM unnecessarily, instead
   * using the first child as the interactive element.
   *
   * @default false
   */
  interactiveChild?: boolean;

  /**
   * Callback function to be called when the tooltip trigger is clicked.
   */
  onClick?: (event: React.MouseEvent) => void;

  /**
   * Callback function to be called when the tooltip trigger is focused.
   */
  onKeyDown?: (event: React.KeyboardEvent) => void;

  /**
   * Side offset for the tooltip.
   */
  sideOffset?: number;

  /**
   * The delay duration (in milliseconds) from when the mouse enters the trigger until the tooltip opens.
   *
   * @default 700
   */
  delayDuration?: number;

  /**
   * If true, the tooltip closes as soon as the pointer leaves the trigger, even when moving through
   * the tooltip content. Prevents the old tooltip staying open when hovering directly between
   * adjacent triggers.
   *
   * @default false
   */
  disableHoverableContent?: boolean;

  className?: string;
}

/**
 * Renders a tooltip with the given content when hovering over its children.
 */
export function Tooltip({
  align,
  arrow = false,
  arrowClassName,
  children,
  className,
  content,
  contentClassName,
  delayDuration,
  disableHoverableContent,
  interactiveChild,
  onClick,
  onKeyDown,
  side,
  sideOffset,
  ...props
}: TooltipProps) {
  return (
    <TooltipPrimitive.Root
      delayDuration={delayDuration}
      disableHoverableContent={disableHoverableContent}
      {...props}
    >
      <TooltipPrimitive.Trigger
        asChild={interactiveChild}
        onClick={onClick}
        onKeyDown={onKeyDown}
        className={className}
      >
        {children}
      </TooltipPrimitive.Trigger>
      <TooltipPrimitive.Portal>
        {/* Do NOT spread {...props} here — the rest object holds Root-scoped props (open,
            onOpenChange, defaultOpen, delayDuration, ...). Radix's Content ultimately renders a
            div, so any Root prop that leaks through would land as an unrecognised DOM attribute
            (open="") or a React "unknown prop onOpenChange on a DOM element" console warning.
            Every Content-specific prop this component supports is passed explicitly below. */}
        <TooltipPrimitive.Content
          className={cn(
            "surface-float z-50 overflow-hidden rounded-md border border-wv-subtle px-3 py-1.5 text-caption shadow-md animate-in fade-in-0 zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2",
            contentClassName,
          )}
          align={align}
          collisionPadding={8}
          side={side}
          sideOffset={sideOffset ?? 6}
        >
          {arrow && (
            <TooltipPrimitive.Arrow className={cn("fill-[var(--wv-bg-surface)]", arrowClassName)} />
          )}
          {content}
        </TooltipPrimitive.Content>
      </TooltipPrimitive.Portal>
    </TooltipPrimitive.Root>
  );
}
