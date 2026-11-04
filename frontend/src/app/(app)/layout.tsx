"use client";

import { AccountMenu } from "@/components/AccountMenu";
import { Logo } from "@/components/Logo";
import { SETTINGS_NAV } from "@/components/settings/nav";
import { DOCS_URL, SAVINGS_METHODOLOGY_URL, SUPPORT_URL, FEEDBACK_URL } from "@/lib/links";
import { api } from "@/lib/api";
import { selfHostedDataSource } from "@/lib/selfHostedDataSource";
import {
  RouterSettingsSidebar,
  RouterSidebar,
  type RouterSidebarNavItem,
  type RouterSidebarUtilityItem,
  RouterSidebarSwap,
  SidebarLayout,
} from "@/shared/components/Sidebar";
import { RouterDashboardDataProvider } from "@/shared/data";

import { usePathname, useRouter } from "next/navigation";

import { useEffect, useState } from "react";

type AuthState = "checking" | "authed" | "anonymous";

// Experiments is hosted-only.
const NAV_ITEMS: readonly RouterSidebarNavItem[] = [
  { href: "/dashboard", icon: "Zap", label: "Dashboard" },
  { href: "/individuals", icon: "UsersRound", label: "Individuals" },
];

const UTILITY_ITEMS: readonly RouterSidebarUtilityItem[] = [
  { href: DOCS_URL, icon: "Book", label: "Docs", newTab: true },
  { href: SAVINGS_METHODOLOGY_URL, icon: "Banknote", label: "Cost savings methodology", newTab: true },
  { href: SUPPORT_URL, icon: "Chat", label: "Support", newTab: true },
  { href: FEEDBACK_URL, icon: "Sparkles", label: "Feedback", newTab: true },
  { href: "/settings", icon: "Settings", label: "Settings" },
];

const SETTINGS_GROUPS = [{ items: SETTINGS_NAV, label: "Router" }];

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const [state, setState] = useState<AuthState>("checking");

  useEffect(() => {
    let cancelled = false;
    api.auth
      .me()
      .then(res => {
        if (cancelled) return;
        if (res.authenticated) {
          setState("authed");
        } else {
          setState("anonymous");
          const next = encodeURIComponent(pathname || "/dashboard");
          router.replace(`/login?next=${next}`);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setState("anonymous");
          router.replace("/login");
        }
      });
    return () => {
      cancelled = true;
    };
  }, [pathname, router]);

  if (state !== "authed") {
    return <SidebarLayout.Loading />;
  }

  const logo = <Logo href="/dashboard" />;
  const accountMenu = <AccountMenu />;
  const sidebar = (
    <RouterSidebarSwap
      settingsPath="/settings"
      main={
        <RouterSidebar
          accountMenu={accountMenu}
          logo={logo}
          navItems={NAV_ITEMS}
          utilityItems={UTILITY_ITEMS}
        />
      }
      settings={
        <RouterSettingsSidebar
          accountMenu={accountMenu}
          dashboardHref="/dashboard"
          exactMatchHrefs={["/settings"]}
          groups={SETTINGS_GROUPS}
          logo={logo}
        />
      }
    />
  );

  return (
    <RouterDashboardDataProvider dataSource={selfHostedDataSource}>
      <SidebarLayout sidebar={sidebar}>{children}</SidebarLayout>
    </RouterDashboardDataProvider>
  );
}
