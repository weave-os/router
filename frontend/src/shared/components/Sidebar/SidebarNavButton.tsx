import { forwardRef } from "react";

import { cn } from "../../tools/cn";
import { Button, ButtonProps } from "../molecules/Button";
import { Appearance } from "../types";

export const SidebarNavButton = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, ...props }, ref) => (
    <Button
      ref={ref}
      appearance={Appearance.Hollow}
      className={cn(
        "sidebar-nav-button h-11 min-h-11 w-full rounded px-2.5 py-0 font-navigation text-wv-secondary hover:bg-wv-hover hover:text-wv-primary focus-visible:ring-inset focus-visible:ring-offset-0 active:bg-wv-active aria-selected:bg-wv-selected aria-selected:text-wv-primary sm:h-8 sm:min-h-8 sm:min-w-11",
        className,
      )}
      {...props}
    />
  ),
);

SidebarNavButton.displayName = "SidebarNavButton";
