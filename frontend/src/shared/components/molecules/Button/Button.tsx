import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";

import Link from "next/link";

import React, { forwardRef } from "react";

import { cn } from "../../../tools/cn";
import { Skeleton } from "../../atoms/Skeleton";
import { Appearance, Intent } from "../../types";

const unfilledButtonStates =
  "hover:bg-wv-hover active:bg-wv-active aria-expanded:bg-wv-selected aria-expanded:hover:bg-wv-hover aria-expanded:active:bg-wv-active aria-selected:bg-wv-selected aria-selected:hover:bg-wv-hover aria-selected:active:bg-wv-active aria-[current=page]:bg-wv-selected aria-[current=page]:hover:bg-wv-hover aria-[current=page]:active:bg-wv-active";

const buttonVariants = cva("button ring-wv-focus ring-offset-wv-surface", {
  defaultVariants: {
    intent: Intent.Default,
    size: "default",
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
      [Appearance.Hollow]: unfilledButtonStates,
    },

    size: {
      default: "h-10 px-4 py-2",
      icon: "size-8",
      lg: "h-11 rounded-md px-8",
      sm: "h-8 rounded-md px-3",
    },
  },
});

export interface ButtonProps
  extends
    React.ButtonHTMLAttributes<HTMLButtonElement>,
    Omit<VariantProps<typeof buttonVariants>, "appearance"> {
  appearance?: Appearance.Hollow | Appearance.Tinted;

  /**
   * If provided, the button will be rendered as an `a` element instead of a `button` element.
   */
  href?: React.ComponentProps<typeof Link>["href"];

  /**
   * If {@link href} is provided, this prop will be passed to the `Link` component.
   * Defaults to `false` for `/api/*` routes to avoid prefetch issues with OAuth redirects.
   *
   * @default true
   */
  prefetch?: boolean;

  /**
   * If {@link href} is provided, this prop will set up the `Link` to open in a new tab.
   *
   * @default false
   */
  newTab?: boolean;
}

type ButtonType = React.ForwardRefExoticComponent<
  React.PropsWithoutRef<ButtonProps> & React.RefAttributes<HTMLButtonElement>
> & {
  Loading: React.ComponentType<LoadingButtonProps>;
};

const BUTTON_COLORS: Record<NonNullable<ButtonProps["appearance"]>, Record<Intent, string>> = {
  [Appearance.Hollow]: {
    [Intent.Danger]: "text-wv-status-danger",
    [Intent.Default]: "text-wv-primary",
    [Intent.Primary]: "text-wv-action",
    [Intent.Success]: "text-wv-status-success",
    [Intent.Warning]: "text-wv-status-warning",
  },
  [Appearance.Tinted]: {
    [Intent.Danger]:
      "bg-wv-status-danger-subtle text-wv-status-danger hover:bg-wv-status-danger active:bg-wv-status-danger-active aria-expanded:bg-wv-status-danger",
    [Intent.Default]:
      "bg-wv-selected text-wv-primary hover:bg-wv-hover active:bg-wv-active aria-expanded:bg-wv-active aria-pressed:bg-wv-active",
    [Intent.Primary]:
      "bg-wv-action-subtle text-wv-action hover:bg-wv-action-subtle-hover active:bg-wv-action-subtle-active aria-expanded:bg-wv-action-subtle-hover",
    [Intent.Success]:
      "bg-wv-status-success-subtle text-wv-status-success hover:bg-wv-status-success active:bg-wv-status-success-active aria-expanded:bg-wv-status-success",
    [Intent.Warning]:
      "bg-wv-status-warning-subtle text-wv-status-warning hover:bg-wv-status-warning active:bg-wv-status-warning-active aria-expanded:bg-wv-status-warning",
  },
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  (
    {
      appearance,
      children,
      className,
      disabled,
      href,
      intent,
      newTab = false,
      prefetch,
      size,
      style,
      ...buttonProps
    },
    ref,
  ) => {
    const isLink = href != null && disabled !== true;
    const isApiRoute = typeof href === "string" && (href === "/api" || href.startsWith("/api/"));

    const buttonChildren = isLink ? (
      <Link
        href={href}
        prefetch={prefetch ?? !isApiRoute}
        {...(newTab ? { rel: "noopener noreferrer", target: "_blank" } : {})}
      >
        {children}
      </Link>
    ) : (
      children
    );

    const As = isLink ? Slot : "button";

    return (
      <As
        {...buttonProps}
        disabled={disabled}
        ref={ref}
        className={cn(
          buttonVariants({
            appearance: appearance === Appearance.Hollow ? appearance : undefined,
            intent,
            size,
          }),
          BUTTON_COLORS[appearance ?? Appearance.Tinted][intent ?? Intent.Default],
          className,
        )}
        style={style}
      >
        {buttonChildren}
      </As>
    );
  },
) as ButtonType;
Button.displayName = "Button";

interface LoadingButtonProps extends Pick<ButtonProps, "className" | "size"> {}

Button.Loading = function LoadingButton({ className, size }: LoadingButtonProps) {
  return (
    <Skeleton
      className={cn(
        buttonVariants({ appearance: Appearance.Hollow, size }),
        {
          "w-20": size === "sm",
          "w-24": size === "default",
          "w-32": size === "lg",
        },
        "block",
        className,
      )}
    />
  );
};
