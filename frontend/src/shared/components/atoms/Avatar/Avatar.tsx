import * as AvatarPrimitive from "@radix-ui/react-avatar";

import React, { useMemo } from "react";

import { cn } from "../../../tools/cn";
import { Skeleton } from "../Skeleton";

export interface AvatarProps extends React.ComponentPropsWithoutRef<typeof AvatarPrimitive.Root> {
  /**
   * The URL of the image to display in the avatar.
   */
  src?: string | null;

  /**
   * The name to display in the avatar, only shown if no image is provided.
   *
   * @example "John Doe"
   */
  name: string;

  /**
   * The shape of the avatar.
   *
   * @default "circle"
   */
  shape?: "circle" | "rounded";

  /**
   * The background color to show in the fallback when no image is provided.
   *
   * @default "muted"
   */
  fallbackBackground?: "background" | "muted" | "primary";

  /**
   * Colors for the fallback initials. When set, it replaces the `fallbackBackground` colors.
   */
  fallbackStyle?: React.CSSProperties;
}

/**
 * Converts a name to fallback text to show in an avatar.
 *
 * @example getFallback("John Doe") // "JD"
 */
function getFallback(name: string): string {
  return name
    .split(" ")
    .filter(part => part.length > 0)
    .map(part => part[0].toUpperCase())
    .join("")
    .slice(0, 2);
}

type AvatarType = React.ForwardRefExoticComponent<
  React.PropsWithoutRef<AvatarProps> & React.RefAttributes<HTMLSpanElement>
> & {
  Loading: typeof AvatarLoading;
};

/**
 * Renders an avatar for a user or other entity.
 */
export const Avatar = React.forwardRef<React.ElementRef<typeof AvatarPrimitive.Root>, AvatarProps>(
  (
    {
      className,
      fallbackBackground = "muted",
      fallbackStyle,
      name,
      shape = "circle",
      src,
      ...props
    },
    ref,
  ) => {
    const fallback = useMemo(() => getFallback(name), [name]);

    return (
      <AvatarPrimitive.Root
        ref={ref}
        className={cn(
          "relative flex size-10 shrink-0 overflow-hidden shadow-sm @container",
          { rounded: shape === "rounded", "rounded-full": shape === "circle" },
          className,
        )}
        {...props}
      >
        {src != null && <AvatarPrimitive.Image className="aspect-square h-full w-full" src={src} />}
        <AvatarPrimitive.Fallback
          className={cn(
            "flex h-full w-full items-center justify-center text-[0.625rem] leading-none @[2rem]:text-body @[3rem]:text-base @[3.5rem]:text-lg @[4rem]:text-xl",
            fallbackStyle == null && fallbackBackground !== "primary" && "text-wv-secondary",
            { rounded: shape === "rounded", "rounded-full": shape === "circle" },
            fallbackStyle == null && {
              "bg-wv-surface": fallbackBackground === "background",
              "bg-wv-placeholder": fallbackBackground === "muted",
              "bg-wv-action text-wv-on-action": fallbackBackground === "primary",
            },
          )}
          style={fallbackStyle}
          delayMs={src != null ? 500 : 0}
        >
          {fallback}
        </AvatarPrimitive.Fallback>
      </AvatarPrimitive.Root>
    );
  },
) as AvatarType;
Avatar.displayName = "Avatar";

export interface AvatarLoadingProps extends Pick<AvatarProps, "className" | "shape"> {}

function AvatarLoading({ className, shape = "circle" }: AvatarLoadingProps) {
  return (
    <Skeleton
      className={cn(
        "relative flex size-10 shrink-0 overflow-hidden shadow-sm",
        { rounded: shape === "rounded", "rounded-full": shape === "circle" },
        className,
      )}
    />
  );
}

Avatar.Loading = AvatarLoading;
