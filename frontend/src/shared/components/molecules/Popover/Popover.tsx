import * as PopoverPrimitive from "@radix-ui/react-popover";

import React from "react";

import { cn } from "../../../tools/cn";
import { SafeOmit } from "../../../tools/types";

type PopoverType = typeof PopoverPrimitive.Root & {
  /**
   * Positions a {@link Popover}'s content against an element other than its trigger.
   */
  Anchor: typeof PopoverPrimitive.Anchor;

  /**
   * Renders the content for a {@link Popover}. Must be a child of a {@link Popover}.
   */
  Content: typeof PopoverContent;

  /**
   * Renders the trigger for a {@link Popover}. Must be a child of a {@link Popover}. Must render an
   * interactive element that forwards its ref as its immediate child (for example, a button).
   */
  Trigger: typeof PopoverTrigger;
};

export type PopoverProps = React.ComponentPropsWithoutRef<typeof PopoverPrimitive.Root>;

/**
 * Renders a popover, with a trigger and content that shows up when clicking the trigger.
 *
 * @example
 * <Popover>
 *   <Popover.Trigger>Trigger</Popover.Trigger>
 *   <Popover.Content>Content</Popover.Content>
 * </Popover>
 */
export const Popover: PopoverType = PopoverPrimitive.Root as PopoverType;

const PopoverTrigger = React.forwardRef<
  React.ElementRef<typeof PopoverPrimitive.Trigger>,
  SafeOmit<React.ComponentPropsWithoutRef<typeof PopoverPrimitive.Trigger>, "asChild">
>(({ className, ...props }, ref) => {
  return (
    <PopoverPrimitive.Trigger
      ref={ref}
      className={cn("cursor-pointer", className)}
      asChild
      {...props}
    />
  );
});
PopoverTrigger.displayName = PopoverPrimitive.Trigger.displayName;

export type PopoverContentProps = React.ComponentPropsWithoutRef<typeof PopoverPrimitive.Content>;

const PopoverContent = React.forwardRef<
  React.ElementRef<typeof PopoverPrimitive.Content>,
  PopoverContentProps
>(({ align = "center", className, sideOffset = 6, ...props }, ref) => (
  <PopoverPrimitive.Portal>
    <PopoverPrimitive.Content
      ref={ref}
      align={align}
      sideOffset={sideOffset}
      collisionPadding={8}
      className={cn(
        "surface-float z-50 w-72 rounded-lg border border-wv-subtle p-4 shadow-md outline-none data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[side=bottom]:slide-in-from-top-2 data-[side=bottom]:slide-out-to-top-1 data-[side=left]:slide-in-from-right-2 data-[side=left]:slide-out-to-right-1 data-[side=right]:slide-in-from-left-2 data-[side=right]:slide-out-to-left-1 data-[side=top]:slide-in-from-bottom-2 data-[side=top]:slide-out-to-bottom-1",
        className,
      )}
      {...props}
    />
  </PopoverPrimitive.Portal>
));
PopoverContent.displayName = PopoverPrimitive.Content.displayName;

Popover.Anchor = PopoverPrimitive.Anchor;
Popover.Trigger = PopoverTrigger;
Popover.Content = PopoverContent;
