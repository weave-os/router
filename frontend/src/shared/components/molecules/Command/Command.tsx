"use client";

import { Command as CommandPrimitive } from "cmdk";

import React, { forwardRef, useCallback, useEffect, useRef } from "react";

import { cn } from "../../../tools/cn";
import { Icon, IconType } from "../../atoms/Icon";
import { Intent } from "../../types";
import { Tooltip } from "../Tooltip";

type CommandType = typeof CommandPrimitive & {
  /**
   * Renders an empty state in a {@link CommandList}. Only shows up when the list is empty.
   */
  Empty: typeof CommandEmpty;

  /**
   * Renders a error state in a {@link CommandList}. Only render this when the list failed to load.
   */
  Error: typeof CommandError;

  /**
   * Renders a group of items in a {@link CommandList}.
   */
  Group: typeof CommandGroup;

  /**
   * Renders the search input for a {@link Command}.
   */
  Input: typeof CommandInput;

  /**
   * Renders a single item in a {@link CommandList}.
   */
  Item: typeof CommandItem;

  /**
   * Renders a list of items in a {@link Command}.
   */
  List: typeof CommandList;

  /**
   * Renders a loading state in a {@link CommandList}. Only render this when the list is loading.
   */
  Loading: typeof CommandLoading;

  /**
   * Renders a separator in a {@link CommandList}.
   */
  Separator: typeof CommandSeparator;

  /**
   * Renders the shortcut for a {@link CommandItem}.
   */
  Shortcut: typeof CommandShortcut;

  /**
   * Renders a warning state in a {@link CommandList}. Use when data loaded partially.
   */
  Warning: typeof CommandWarning;
};

/**
 * Renders a searchable list of items.
 *
 * @example
 * <Command>
 *   <Command.Input placeholder="Type a command or search..." />
 *   <Command.List>
 *     <Command.Empty>No results found.</CommandEmpty>
 *     <Command.Group heading="Suggestions">
 *       <Command.Item>
 *         Calendar
 *         <Command.Shortcut>⌘K</Command.Shortcut>
 *       </CommandItem>
 *       <Command.Item>Search Emoji</CommandItem>
 *       <Command.Item>Calculator</CommandItem>
 *     </Command.Group>
 *     <Command.Separator />
 *     <Command.Group heading="Settings">
 *       <Command.Item>Profile</CommandItem>
 *       <Command.Item>Billing</CommandItem>
 *       <Command.Item>Settings</CommandItem>
 *     </Command.Group>
 *   </Command.List>
 * </Command>
 */
export const Command = forwardRef<
  React.ElementRef<typeof CommandPrimitive>,
  React.ComponentPropsWithoutRef<typeof CommandPrimitive>
>(({ className, ...props }, ref) => (
  <CommandPrimitive
    ref={ref}
    loop
    className={cn("flex h-full w-full flex-col text-wv-primary", className)}
    {...props}
  />
)) as CommandType;
Command.displayName = CommandPrimitive.displayName;

interface CommandInputProps extends React.ComponentPropsWithoutRef<typeof CommandPrimitive.Input> {
  /**
   * If provided, renders an icon to the left of the input.
   *
   * Pass `null` to render no icon.
   *
   * @default "Search"
   */
  icon?: IconType | null;

  /**
   * Class name to pass to the container element. Note that the `className` prop will go to the
   * actual input element.
   */
  containerClassName?: string;
}

const CommandInput = forwardRef<React.ElementRef<typeof CommandPrimitive.Input>, CommandInputProps>(
  ({ children, className, containerClassName, icon = "Search", ...props }, ref) => (
    <div
      className={cn("flex items-center gap-2 border-b border-wv-subtle px-3", containerClassName)}
      cmdk-input-wrapper=""
    >
      {icon != null && <Icon type="Search" className="size-4 shrink-0 text-wv-placeholder" />}
      {children}
      <CommandPrimitive.Input
        ref={ref}
        className={cn(
          "flex h-11 w-full rounded-md bg-transparent py-3 text-body text-wv-primary outline-none placeholder:text-wv-placeholder disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
        {...props}
      />
    </div>
  ),
);
CommandInput.displayName = CommandPrimitive.Input.displayName;

const CommandList = forwardRef<
  React.ElementRef<typeof CommandPrimitive.List>,
  React.ComponentPropsWithoutRef<typeof CommandPrimitive.List>
>(({ className, ...props }, ref) => {
  const internalRef = useRef<HTMLDivElement | null>(null);

  const shouldScrollSelectedIntoView = useRef(false);

  // Fix for cmdk scrolling issue: when filtering, cmdk reorders DOM elements via appendChild,
  // which would let browser scroll anchoring shift the scroll position (hence `overflow-anchor:
  // none`). Typing returns the list to the top, and the selected item is scrolled into view
  // whenever keyboard navigation flips its `aria-selected` attribute. Pointer hover also flips
  // `aria-selected`, but should never scroll the list under the cursor.
  useEffect(() => {
    const el = internalRef.current;
    if (el == null) return;

    const root = el.closest<HTMLElement>("[cmdk-root]") ?? el.parentElement;
    let rafId: number | undefined;

    const enableSelectionScroll = () => {
      shouldScrollSelectedIntoView.current = true;
    };

    // A new query selects the first result, so once cmdk has re-ranked (and possibly scrolled to the
    // previous selection) the list starts again from the top. Scrolling the selected row into view
    // instead would chase it mid-slide while results animate into rank.
    let scrollResetFrameId: number | undefined;
    const resetScrollForNewQuery = () => {
      if (scrollResetFrameId !== undefined) cancelAnimationFrame(scrollResetFrameId);
      scrollResetFrameId = requestAnimationFrame(() => {
        scrollResetFrameId = undefined;
        el.scrollTop = 0;
      });
    };

    const enableKeyboardSelectionScroll = (event: KeyboardEvent) => {
      if (["ArrowDown", "ArrowUp", "End", "Home", "PageDown", "PageUp"].includes(event.key)) {
        enableSelectionScroll();
      }
    };

    const disablePointerSelectionScroll = () => {
      shouldScrollSelectedIntoView.current = false;
      if (rafId !== undefined) {
        cancelAnimationFrame(rafId);
        rafId = undefined;
      }
    };

    root?.addEventListener("input", resetScrollForNewQuery, true);
    root?.addEventListener("keydown", enableKeyboardSelectionScroll, true);
    el.addEventListener("pointermove", disablePointerSelectionScroll, true);

    const observer = new MutationObserver(() => {
      if (!shouldScrollSelectedIntoView.current) return;

      if (rafId !== undefined) cancelAnimationFrame(rafId);
      rafId = requestAnimationFrame(() => {
        rafId = undefined;
        shouldScrollSelectedIntoView.current = false;
        const selected = el.querySelector<HTMLElement>('[aria-selected="true"]');
        if (selected != null) {
          selected.scrollIntoView({ block: "nearest" });
        }
      });
    });

    observer.observe(el, {
      attributeFilter: ["aria-selected"],
      attributes: true,
      subtree: true,
    });

    return () => {
      root?.removeEventListener("input", resetScrollForNewQuery, true);
      root?.removeEventListener("keydown", enableKeyboardSelectionScroll, true);
      el.removeEventListener("pointermove", disablePointerSelectionScroll, true);
      observer.disconnect();
      if (rafId !== undefined) cancelAnimationFrame(rafId);
      if (scrollResetFrameId !== undefined) cancelAnimationFrame(scrollResetFrameId);
    };
  }, []);

  const setRef = useCallback(
    (node: HTMLDivElement | null) => {
      internalRef.current = node;
      if (typeof ref === "function") ref(node);
      else if (ref != null) ref.current = node;
    },
    [ref],
  );

  return (
    <CommandPrimitive.List
      ref={setRef}
      className={cn(
        "max-h-96 scroll-p-4 overflow-y-auto overflow-x-hidden [overflow-anchor:none]",
        className,
      )}
      {...props}
    />
  );
});
CommandList.displayName = CommandPrimitive.List.displayName;

const CommandEmpty = forwardRef<
  React.ElementRef<typeof CommandPrimitive.Empty>,
  React.ComponentPropsWithoutRef<typeof CommandPrimitive.Empty>
>((props, ref) => (
  <CommandPrimitive.Empty
    ref={ref}
    className="py-6 text-center text-body text-wv-secondary"
    {...props}
  />
));
CommandEmpty.displayName = CommandPrimitive.Empty.displayName;

const CommandGroup = forwardRef<
  React.ElementRef<typeof CommandPrimitive.Group>,
  React.ComponentPropsWithoutRef<typeof CommandPrimitive.Group>
>(({ className, ...props }, ref) => (
  <CommandPrimitive.Group
    ref={ref}
    className={cn(
      "overflow-hidden p-1 text-wv-primary [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-caption [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-wv-secondary",
      className,
    )}
    {...props}
  />
));
CommandGroup.displayName = CommandPrimitive.Group.displayName;

const CommandSeparator = forwardRef<
  React.ElementRef<typeof CommandPrimitive.Separator>,
  React.ComponentPropsWithoutRef<typeof CommandPrimitive.Separator>
>(({ className, ...props }, ref) => (
  <CommandPrimitive.Separator
    ref={ref}
    alwaysRender
    className={cn("h-px bg-wv-divider first:hidden last:hidden", className)}
    {...props}
  />
));
CommandSeparator.displayName = CommandPrimitive.Separator.displayName;

const CommandItem = forwardRef<
  React.ElementRef<typeof CommandPrimitive.Item>,
  React.ComponentPropsWithoutRef<typeof CommandPrimitive.Item>
>(({ className, ...props }, ref) => (
  <CommandPrimitive.Item
    ref={ref}
    className={cn(
      "relative flex min-w-0 cursor-pointer select-none items-center gap-2 rounded p-2 text-body outline-none aria-selected:bg-wv-selected aria-selected:text-wv-primary data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50",
      className,
    )}
    {...props}
  />
));
CommandItem.displayName = CommandPrimitive.Item.displayName;

const CommandShortcut = ({ className, ...props }: React.HTMLAttributes<HTMLSpanElement>) => {
  return (
    <span
      className={cn("ml-auto text-caption tracking-widest text-wv-secondary", className)}
      {...props}
    />
  );
};
CommandShortcut.displayName = "CommandShortcut";

interface CommandStatusProps extends React.ComponentPropsWithoutRef<
  typeof CommandPrimitive.Loading
> {
  /**
   * If provided, wraps the content in a tooltip with this message.
   */
  tooltip?: React.ReactNode;
}

interface InternalCommandStatusProps extends CommandStatusProps {
  icon: IconType;
  intent: Intent;
}

const CommandStatus = forwardRef<
  React.ElementRef<typeof CommandPrimitive.Loading>,
  InternalCommandStatusProps
>(({ children, className, icon, intent, tooltip, ...props }, ref) => {
  const content = (
    <CommandPrimitive.Loading
      ref={ref}
      className={cn(
        "relative cursor-default select-none items-center gap-2 rounded p-2 text-body text-wv-secondary",
        className,
      )}
      {...props}
    >
      <div className="flex flex-row items-center gap-2">
        <Icon type={icon} className="size-4" intent={intent} />
        {children}
      </div>
    </CommandPrimitive.Loading>
  );

  return (
    <CommandGroup>
      {tooltip != null ? (
        <Tooltip content={tooltip} interactiveChild>
          {content}
        </Tooltip>
      ) : (
        content
      )}
    </CommandGroup>
  );
});
CommandStatus.displayName = "CommandStatus";

const CommandError = forwardRef<
  React.ElementRef<typeof CommandPrimitive.Loading>,
  CommandStatusProps
>((props, ref) => <CommandStatus ref={ref} icon="CircleAlert" intent={Intent.Danger} {...props} />);
CommandError.displayName = "CommandError";

const CommandWarning = forwardRef<
  React.ElementRef<typeof CommandPrimitive.Loading>,
  CommandStatusProps
>((props, ref) => (
  <CommandStatus ref={ref} icon="TriangleAlert" intent={Intent.Warning} {...props} />
));
CommandWarning.displayName = "CommandWarning";

const CommandLoading = forwardRef<
  React.ElementRef<typeof CommandPrimitive.Loading>,
  React.ComponentPropsWithoutRef<typeof CommandPrimitive.Loading>
>(({ children, className, ...props }, ref) => (
  <CommandGroup>
    <CommandPrimitive.Loading
      ref={ref}
      className={cn(
        "relative cursor-default select-none items-center gap-2 rounded p-2 text-body text-wv-secondary",
        className,
      )}
      {...props}
    >
      <div className="flex flex-row items-center gap-2">
        <Icon type="Loading" className="size-4 animate-spin" />
        {children}
      </div>
    </CommandPrimitive.Loading>
  </CommandGroup>
));
CommandLoading.displayName = CommandPrimitive.Loading.displayName;

Command.Empty = CommandEmpty;
Command.Error = CommandError;
Command.Group = CommandGroup;
Command.Input = CommandInput;
Command.Item = CommandItem;
Command.List = CommandList;
Command.Loading = CommandLoading;
Command.Separator = CommandSeparator;
Command.Shortcut = CommandShortcut;
Command.Warning = CommandWarning;
