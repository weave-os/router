"use client";

import { type ReactNode, useEffect } from "react";

import { ElementClassName } from "../../../ids";
import { copyToClipboard } from "../../../tools/clipboard";
import { cn } from "../../../tools/cn";
import { buildShareUrl } from "../../../tools/shareUrl";
import { Icon } from "../../atoms/Icon";
import { Appearance } from "../../types";
import { Button } from "../Button";
import { Modal, type ModalContentProps, SidebarModalContent } from "../Modal";

type DrawerContentProps = Omit<ModalContentProps, "hideCloseButton">;

function DrawerContent({ className, ...props }: DrawerContentProps) {
  return (
    <SidebarModalContent
      {...props}
      hideCloseButton
      className={cn("flex-col sm:max-w-xl", className)}
    />
  );
}

interface DrawerToolbarProps {
  /** Controls at the right end, e.g. a copy-link button. */
  actions?: ReactNode;
  /** Controls after the close button, e.g. {@link Drawer.Pager}. */
  children?: ReactNode;
  closeLabel: string;
}

/** The slim fixed bar at the top of every drawer; the close button always leads it. */
function DrawerToolbar({ actions, children, closeLabel }: DrawerToolbarProps) {
  return (
    <div className="flex h-11 shrink-0 items-center gap-0.5 border-b px-2">
      <Modal.Close asChild>
        <Button appearance={Appearance.Hollow} aria-label={closeLabel} size="icon">
          <Icon type="ArrowRightToLine" className="size-4" aria-hidden="true" />
        </Button>
      </Modal.Close>
      <div className="flex min-w-0 flex-1 items-center gap-0.5">{children}</div>
      {actions}
    </div>
  );
}

interface DrawerPagerProps {
  count: number;
  index: number;
  /** Names the items in the step buttons' labels, e.g. "attribute" → "Next attribute". */
  itemLabel: string;
  onIndexChange: (index: number) => void;
}

/** Previous/next buttons and "N of M" for a toolbar; ←/→ step too, except while typing. */
function DrawerPager({ count, index, itemLabel, onIndexChange }: DrawerPagerProps) {
  const hasPrevious = index > 0;
  const hasNext = index < count - 1;

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return;
      if (event.shiftKey) return;
      const target = event.target;
      if (
        target instanceof HTMLElement &&
        (target.isContentEditable || target.closest("input, textarea, select") != null)
      ) {
        return;
      }
      if (event.key === "ArrowLeft" && hasPrevious) onIndexChange(index - 1);
      else if (event.key === "ArrowRight" && hasNext) onIndexChange(index + 1);
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [hasNext, hasPrevious, index, onIndexChange]);

  return (
    <>
      {/* aria-disabled, not disabled: a disabled button drops focus at the first or last item. */}
      <Button
        appearance={Appearance.Hollow}
        aria-disabled={!hasPrevious}
        aria-label={`Previous ${itemLabel}`}
        className="aria-disabled:cursor-default aria-disabled:opacity-50"
        onClick={() => {
          if (hasPrevious) onIndexChange(index - 1);
        }}
        size="icon"
      >
        <Icon type="ChevronLeft" className="size-4" aria-hidden="true" />
      </Button>
      <Button
        appearance={Appearance.Hollow}
        aria-disabled={!hasNext}
        aria-label={`Next ${itemLabel}`}
        className="aria-disabled:cursor-default aria-disabled:opacity-50"
        onClick={() => {
          if (hasNext) onIndexChange(index + 1);
        }}
        size="icon"
      >
        <Icon type="ChevronRight" className="size-4" aria-hidden="true" />
      </Button>
      <span className="ml-1 text-body text-wv-secondary">
        <span className="tabular-nums">{index + 1}</span> of{" "}
        <span className="tabular-nums">{count}</span>
      </span>
    </>
  );
}

interface DrawerCopyLinkProps {
  /** The URL param that reopens this drawer when the page loads. */
  param: string;
  value: string;
}

/** Copies the page's URL with {@link DrawerCopyLinkProps.param} set, like a chart's "Copy link to chart". */
function DrawerCopyLink({ param, value }: DrawerCopyLinkProps) {
  return (
    <Button
      appearance={Appearance.Hollow}
      aria-label="Copy link"
      onClick={() => void copyToClipboard(buildShareUrl(param, value))}
      size="icon"
    >
      <Icon type="Link" className="size-4" aria-hidden="true" />
    </Button>
  );
}

interface DrawerHeaderProps {
  description?: ReactNode;
  eyebrow?: ReactNode;
  title: ReactNode;
}

/** The drawer's title block; it scrolls with {@link Drawer.Body} below the toolbar. */
function DrawerHeader({ description, eyebrow, title }: DrawerHeaderProps) {
  return (
    <Modal.Header className="gap-1 text-left">
      {eyebrow != null && <div className="text-body text-wv-secondary empty:hidden">{eyebrow}</div>}
      <Modal.Title className="leading-snug">{title}</Modal.Title>
      {description != null && <Modal.Description>{description}</Modal.Description>}
    </Modal.Header>
  );
}

/** The drawer's scrolling region; `?chart=` deep links center their chart inside it. */
function DrawerBody({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      {...props}
      className={cn(
        "flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto overscroll-contain p-4",
        ElementClassName.ScrollableContainer,
        className,
      )}
    />
  );
}

/**
 * A right-side drawer. Radix props on {@link Drawer.Content} (`onOpenAutoFocus`,
 * `onCloseAutoFocus`, `onEscapeKeyDown`, …) pass through, so callers keep their own focus return.
 *
 * @example
 *  <Drawer open={open} onOpenChange={setOpen}>
 *    <Drawer.Content>
 *      <Drawer.Toolbar closeLabel="Close insights" />
 *      <Drawer.Body>
 *        <Drawer.Header eyebrow="Week of Jan 6" title="Insights" />
 *        …
 *      </Drawer.Body>
 *    </Drawer.Content>
 *  </Drawer>
 */
export const Drawer = Object.assign(
  (props: React.ComponentProps<typeof Modal>) => <Modal {...props} />,
  {
    Body: DrawerBody,
    Content: DrawerContent,
    CopyLink: DrawerCopyLink,
    Header: DrawerHeader,
    Pager: DrawerPager,
    Toolbar: DrawerToolbar,
  },
);
