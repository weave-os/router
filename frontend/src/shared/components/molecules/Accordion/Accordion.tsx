"use client";

import * as AccordionPrimitive from "@radix-ui/react-accordion";

import * as React from "react";

import { cn } from "../../../tools/cn";
import { Icon } from "../../atoms/Icon";

type AccordionType = typeof AccordionPrimitive.Root & {
  /**
   * Renders the content for an accordion item. Should be a child of an {@link Accordion.Item}.
   * Children will not be rendered if the item is collapsed, unless the `forceMount` prop is set.
   */
  Content: typeof AccordionContent;

  /**
   * Renders the header for an accordion item. Should be a child of an {@link Accordion.Item}. Use
   * `asChild`/`Text` to customize which header level is used.
   */
  Header: typeof AccordionHeader;

  /**
   * Renders an item in an accordion. An item can expand and contract based on user interaction.
   *
   * @example
   * <Accordion.Item>
   *   <Accordion.Header>
   *     <Accordion.Trigger>Click me</Accordion.Trigger>
   *   </Accordion.Header>
   *   <Accordion.Content>Content goes here</Accordion.Content>
   * </Accordion.Item>
   */
  Item: typeof AccordionItem;

  /**
   * Renders a clickable trigger for an accordion item. When clicked, the item will expand or
   * contract. Should be a child of {@link Accordion.Header}.
   */
  Trigger: typeof AccordionTrigger;

  /**
   * Renders an icon to put in the {@link Accordion.Trigger} to indicate the state of the accordion.
   */
  TriggerIcon: typeof AccordionTriggerIcon;
};

/**
 * Renders an accordion component, composed of items that can expand and contract.
 */
export const Accordion: AccordionType = AccordionPrimitive.Root as AccordionType;

const AccordionItem = React.forwardRef<
  React.ElementRef<typeof AccordionPrimitive.Item>,
  React.ComponentPropsWithoutRef<typeof AccordionPrimitive.Item>
>(({ className, ...props }, ref) => (
  <AccordionPrimitive.Item ref={ref} className={className} {...props} />
));
AccordionItem.displayName = "AccordionItem";

const AccordionHeader = React.forwardRef<
  React.ElementRef<typeof AccordionPrimitive.Header>,
  React.ComponentPropsWithoutRef<typeof AccordionPrimitive.Header>
>(({ children, className, ...props }, ref) => (
  <AccordionPrimitive.Header className={cn("flex", className)} ref={ref} {...props}>
    {children}
  </AccordionPrimitive.Header>
));
AccordionHeader.displayName = AccordionPrimitive.Header.displayName;

const AccordionTrigger = React.forwardRef<
  React.ElementRef<typeof AccordionPrimitive.Trigger>,
  React.ComponentPropsWithoutRef<typeof AccordionPrimitive.Trigger>
>(({ children, className, ...props }, ref) => (
  <AccordionPrimitive.Trigger
    ref={ref}
    className={cn(
      "group/accordion flex flex-1 flex-row items-center gap-2 font-medium transition-all hover:underline",
      className,
    )}
    {...props}
  >
    {children}
  </AccordionPrimitive.Trigger>
));
AccordionTrigger.displayName = AccordionPrimitive.Trigger.displayName;

function AccordionTriggerIcon({ className, ...props }: React.HTMLAttributes<SVGElement>) {
  return (
    <Icon
      className={cn(
        "size-4 shrink-0 grow-0 transition-transform duration-200 group-data-[state=closed]/accordion:-rotate-90",
        className,
      )}
      {...props}
      type="ChevronDown"
    />
  );
}

type AccordionContentProps = React.ComponentPropsWithoutRef<typeof AccordionPrimitive.Content> & {
  /** Classes for the Radix content element itself (the height-animated wrapper), not the padded inner div. */
  outerClassName?: string;
};

const AccordionContent = React.forwardRef<
  React.ElementRef<typeof AccordionPrimitive.Content>,
  AccordionContentProps
>(({ children, className, outerClassName, ...props }, ref) => (
  <AccordionPrimitive.Content
    ref={ref}
    className={cn(
      "overflow-hidden text-body transition-all data-[state=closed]:animate-accordion-up data-[state=open]:animate-accordion-down",
      outerClassName,
    )}
    {...props}
  >
    <div className={cn("pb-4 pt-0", className)}>{children}</div>
  </AccordionPrimitive.Content>
));
AccordionContent.displayName = AccordionPrimitive.Content.displayName;

Accordion.Item = AccordionItem;
Accordion.Header = AccordionHeader;
Accordion.Trigger = AccordionTrigger;
Accordion.TriggerIcon = AccordionTriggerIcon;
Accordion.Content = AccordionContent;
