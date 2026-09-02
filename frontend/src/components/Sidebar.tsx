"use client";

import { Logo } from "./Logo";
import { Button } from "@/components/molecules/Button";
import { Tooltip } from "@/components/molecules/Tooltip";
import { Appearance } from "@/components/types";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Activity, BarChart2, FlaskConical, LogOut, Settings, Waypoints } from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { type ReactNode } from "react";

interface NavItem {
  href: string;
  label: string;
  icon: ReactNode;
  matchPrefix?: string;
}

const PRIMARY_NAV: NavItem[] = [
  { href: "/dashboard", label: "Overview", icon: <BarChart2 size={16} /> },
  { href: "/settings/providers", label: "Providers", icon: <Waypoints size={16} /> },
  { href: "/settings/models", label: "Models & routing", icon: <Activity size={16} /> },
  { href: "/route-lab", label: "Route Lab", icon: <FlaskConical size={16} /> },
];

function NavLink({ item }: { item: NavItem }) {
  const pathname = usePathname();
  const active =
    item.matchPrefix != null
      ? pathname.startsWith(item.matchPrefix)
      : pathname === item.href || pathname.startsWith(item.href + "/");

  return (
    <Link
      href={item.href}
      aria-selected={active}
      title={item.label}
      className={cn(
        "relative flex h-9 w-full items-center gap-2 rounded-md border border-transparent px-3 text-xs font-medium text-muted-foreground transition-colors",
        "hover:bg-foreground/5 hover:text-foreground",
        "aria-selected:border-border aria-selected:bg-foreground/[0.04] aria-selected:text-foreground",
      )}
    >
      <span className="shrink-0">{item.icon}</span>
      <span className="hidden whitespace-nowrap md:inline">{item.label}</span>
    </Link>
  );
}

export function Sidebar() {
  const router = useRouter();

  async function handleSignOut() {
    try {
      await api.auth.logout();
    } catch {
      // Best-effort: even if the network call fails, redirect to /login so
      // the user is no longer in a half-authed state.
    }
    router.replace("/login");
  }

  return (
    <div className="group/sidebar relative flex h-full w-12 shrink-0 grow-0 flex-col items-start gap-1 overflow-hidden transition-all duration-200 ease-out md:w-[244px] md:overflow-visible">
      <header className="relative z-10 flex w-full flex-col items-center gap-3 border-b border-border/70 py-2 pb-3 transition-all duration-200 md:flex-row md:pl-2 md:pr-3 md:pt-2">
        <Logo href="/dashboard" />
        <div className="hidden min-w-0 md:block">
          <p className="truncate text-sm font-semibold tracking-tight text-foreground">Router</p>
          <p className="router-mono text-[10px] uppercase tracking-wider text-muted-foreground">control plane</p>
        </div>
      </header>

      <nav className="relative z-10 flex w-full flex-1 flex-col gap-1 overflow-y-auto md:p-2 md:pt-0">
        {PRIMARY_NAV.map(item => (
          <NavLink key={item.href} item={item} />
        ))}
      </nav>

      <div className="relative z-10 flex w-full items-center justify-between gap-2 border-t border-border/70 p-2 pt-3">
        <Tooltip content="Settings" side="right" interactiveChild>
          <Button
            href="/settings"
            appearance={Appearance.Hollow}
            className={sidebarFooterButton}
          >
            <Settings className="size-4" />
          </Button>
        </Tooltip>

        <Tooltip content="Sign out" side="left" interactiveChild>
          <Button
            appearance={Appearance.Hollow}
            className={sidebarFooterButton}
            onClick={() => {
              void handleSignOut();
            }}
          >
            <LogOut className="size-4" />
          </Button>
        </Tooltip>
      </div>
    </div>
  );
}

// Identical visual treatment for both footer buttons so the only difference
// the user sees is the icon. Kept as a constant to guarantee the two stay in
// lockstep when the design changes.
const sidebarFooterButton =
  "size-8 shrink-0 justify-center rounded-md border border-border-darker bg-muted p-0 text-muted-foreground hover:bg-border-darker hover:text-foreground";
