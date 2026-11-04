import type { ComponentProps } from "react";

import { Icon } from "../atoms/Icon";
import { cn } from "../../tools/cn";

interface SidebarNavIconProps extends ComponentProps<typeof Icon> {
  active?: boolean;
}

export function SidebarNavIcon({ active = false, className, ...props }: SidebarNavIconProps) {
  return (
    <Icon className={cn("size-3.5 shrink-0", active && "text-wv-primary", className)} {...props} />
  );
}
