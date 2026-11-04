import { cva, type VariantProps } from "class-variance-authority";

import Link from "next/link";

import * as React from "react";

import { cn } from "../../../tools/cn";
import { SafeOmit } from "../../../tools/types";
import { Skeleton } from "../../atoms/Skeleton";
import { Appearance, Intent } from "../../types";
import { Tooltip } from "../Tooltip";

const badgeVariants = cva(
  "inline-flex shrink-0 grow-0 cursor-default items-center gap-1 truncate rounded-full px-2.5 py-0.5 text-body font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-wv-focus focus-visible:ring-offset-2 focus-visible:ring-offset-wv-surface",
  {
    defaultVariants: {
      appearance: Appearance.Outlined,
      intent: Intent.Default,
    },

    variants: {
      intent: {
        [Intent.Danger]: "",
        [Intent.Default]: "",
        [Intent.Primary]: "",
        [Intent.Success]: "",
        [Intent.Warning]: "",
      },

      appearance: {
        [Appearance.Tinted]: "",
        [Appearance.Outlined]: "border",
      },
    },
  },
);

const BADGE_COLORS: Record<NonNullable<BadgeProps["appearance"]>, Record<Intent, string>> = {
  [Appearance.Tinted]: {
    [Intent.Danger]: "bg-wv-status-danger-subtle text-wv-status-danger",
    [Intent.Default]: "bg-wv-selected text-wv-secondary",
    [Intent.Primary]: "bg-wv-action-subtle text-wv-action",
    [Intent.Success]: "bg-wv-status-success-subtle text-wv-status-success",
    [Intent.Warning]: "bg-wv-status-warning-subtle text-wv-status-warning",
  },
  [Appearance.Outlined]: {
    [Intent.Danger]:
      "border-wv-status-danger-subtle bg-wv-status-danger-subtle text-wv-status-danger",
    [Intent.Default]: "border-wv-subtle bg-wv-surface text-wv-secondary",
    [Intent.Primary]: "border-wv-action-subtle bg-wv-action-subtle text-wv-action",
    [Intent.Success]:
      "border-wv-status-success-subtle bg-wv-status-success-subtle text-wv-status-success",
    [Intent.Warning]:
      "border-wv-status-warning-subtle bg-wv-status-warning-subtle text-wv-status-warning",
  },
};

const LINK_STATE_COLORS: Record<Intent, string> = {
  [Intent.Danger]: "hover:bg-wv-status-danger active:bg-wv-status-danger-active",
  [Intent.Default]: "hover:bg-wv-hover active:bg-wv-active",
  [Intent.Primary]: "hover:bg-wv-action-subtle-hover active:bg-wv-action-subtle-active",
  [Intent.Success]: "hover:bg-wv-status-success active:bg-wv-status-success-active",
  [Intent.Warning]: "hover:bg-wv-status-warning active:bg-wv-status-warning-active",
};

export interface BadgeProps
  extends
    SafeOmit<React.HTMLAttributes<HTMLDivElement>, "color">,
    Omit<VariantProps<typeof badgeVariants>, "appearance"> {
  appearance?: Appearance.Outlined | Appearance.Tinted;

  /**
   * If provided, will render a tooltip when the user hovers over the badge or focuses it with
   * the keyboard.
   */
  tooltip?: React.ReactNode;

  /**
   * If provided, the badge will have the provided color. Overrides intent and appearance styles.
   *
   * @example "221 63% 55%"
   */
  hslColor?: string;

  /**
   * If provided, renders the badge as a link. Supports Next.js Link href values.
   */
  href?: string;
}

type BadgeType = React.ForwardRefExoticComponent<
  React.PropsWithoutRef<BadgeProps> & React.RefAttributes<HTMLDivElement>
> & {
  Loading: React.ComponentType<BadgeLoadingProps>;
};

/**
 * Renders a badge.
 */
export const Badge = React.forwardRef<HTMLDivElement, BadgeProps>(
  ({ appearance, className, href, hslColor, intent, style, tooltip, ...props }, ref) => {
    const hasTooltip = tooltip != null;
    const isLink = href != null;

    const sharedClassName = cn(
      badgeVariants({ appearance, intent }),
      hslColor != null
        ? "border-[var(--badge-border-color)] bg-[var(--badge-background)] text-[hsl(var(--badge-text-color))]"
        : BADGE_COLORS[appearance ?? Appearance.Outlined][intent ?? Intent.Default],
      isLink && "cursor-pointer",
      isLink &&
        (hslColor != null
          ? "hover:bg-[var(--badge-hover-color)] active:bg-[var(--badge-active-color)]"
          : LINK_STATE_COLORS[intent ?? Intent.Default]),
      className,
    );

    const sharedStyle = {
      ...style,
      ...(hslColor != null
        ? {
            "--badge-active-color": `color-mix(in srgb, hsl(${hslColor}) ${appearance === Appearance.Tinted ? 18 : 14}%, var(--wv-bg-surface))`,
            "--badge-background": `color-mix(in srgb, hsl(${hslColor}) ${appearance === Appearance.Tinted ? 10 : 5}%, var(--wv-bg-surface))`,
            "--badge-border-color": `color-mix(in srgb, hsl(${hslColor}) 12%, var(--wv-bg-surface))`,
            "--badge-hover-color": `color-mix(in srgb, hsl(${hslColor}) ${appearance === Appearance.Tinted ? 14 : 10}%, var(--wv-bg-surface))`,
            "--badge-text-color": hslColor,
          }
        : {}),
    };

    const result = isLink ? (
      <Link
        href={href}
        className={sharedClassName}
        style={sharedStyle}
        tabIndex={hasTooltip ? 0 : undefined}
      >
        {props.children}
      </Link>
    ) : (
      <div
        ref={ref}
        className={sharedClassName}
        tabIndex={hasTooltip ? 0 : undefined}
        style={sharedStyle}
        {...props}
      />
    );

    if (hasTooltip) {
      return (
        <Tooltip content={tooltip} disableHoverableContent interactiveChild>
          {result}
        </Tooltip>
      );
    }

    return result;
  },
) as BadgeType;
Badge.displayName = "Badge";

interface BadgeLoadingProps {
  className?: string;
}

Badge.Loading = function BadgeLoading({ className }: BadgeLoadingProps) {
  return (
    <Skeleton className={cn("m-px h-6 w-20 rounded-full border border-wv-default", className)} />
  );
};
