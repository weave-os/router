import type { ComponentProps } from "react";

import { cn } from "../../tools/cn";
import { Icon } from "../atoms/Icon";

interface SidebarNavIconProps extends ComponentProps<typeof Icon> {
  active?: boolean;
}

export function SidebarNavIcon({ active = false, className, ...props }: SidebarNavIconProps) {
  return (
    <Icon className={cn("size-3.5 shrink-0", active && "text-wv-primary", className)} {...props} />
  );
}
