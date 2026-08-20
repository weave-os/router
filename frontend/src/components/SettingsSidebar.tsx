"use client";

import { Logo } from "./Logo";
import { Button } from "@/components/molecules/Button";
import { SETTINGS_NAV, type SettingsNavItem } from "@/components/settings/nav";
import { Appearance } from "@/components/types";
import { cn } from "@/lib/cn";
import { ChevronLeft } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";

function NavLink({ item }: { item: SettingsNavItem }) {
  const pathname = usePathname();
  // /settings is the index tab, so it has to match exactly or every nested tab
  // would light it up too.
  const active =
    item.href === "/settings"
      ? pathname === item.href
      : pathname === item.href || pathname.startsWith(item.href + "/");

  return (
    <Link
      href={item.href}
      aria-selected={active}
      title={item.label}
      className={cn(
        "relative flex h-8 w-full items-center gap-2 rounded-md px-3 text-xs font-medium text-muted-foreground transition-colors",
        "hover:bg-foreground/5 hover:text-foreground",
        "aria-selected:bg-foreground/5 aria-selected:text-foreground",
      )}
    >
      <span className="shrink-0">{item.icon}</span>
      <span className="hidden whitespace-nowrap md:inline">{item.label}</span>
    </Link>
  );
}

/**
 * Settings nav, shown in place of the dashboard sidebar while the user is
 * under /settings.
 */
export function SettingsSidebar() {
  return (
    <div className="group/sidebar relative flex h-full w-12 shrink-0 grow-0 flex-col items-start gap-1 overflow-hidden transition-all duration-200 ease-out md:w-[244px] md:overflow-visible">
      <header className="relative z-10 flex w-full flex-col items-center gap-4 py-2 transition-all duration-200 md:flex-row md:pl-2 md:pr-3 md:pt-2">
        <Logo href="/dashboard" />
      </header>

      <nav className="relative z-10 flex w-full flex-1 flex-col gap-2 overflow-y-auto md:p-2 md:pt-0">
        <Button
          href="/dashboard"
          appearance={Appearance.Hollow}
          className="h-8 w-full justify-center px-3 text-xs text-muted-foreground md:justify-start"
        >
          <ChevronLeft className="size-4 shrink-0" />
          <span className="hidden whitespace-nowrap md:inline">Back to dashboard</span>
        </Button>

        <div className="flex w-full flex-col gap-1">
          <p className="hidden px-3 py-1 text-2xs font-medium uppercase tracking-wide text-muted-foreground md:block">
            Settings
          </p>
          {SETTINGS_NAV.map(item => (
            <NavLink key={item.href} item={item} />
          ))}
        </div>
      </nav>
    </div>
  );
}
