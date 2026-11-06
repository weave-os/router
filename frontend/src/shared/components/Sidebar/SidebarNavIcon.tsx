import type { ComponentProps } from "react";

import { Icon } from "@weave-os/ui/components/atoms/Icon";
import { cn } from "@weave-os/ui/tools/cn";

interface SidebarNavIconProps extends ComponentProps<typeof Icon> {
  active?: boolean;
}

export function SidebarNavIcon({ active = false, className, ...props }: SidebarNavIconProps) {
  return (
    <Icon className={cn("size-3.5 shrink-0", active && "text-wv-primary", className)} {...props} />
  );
}
