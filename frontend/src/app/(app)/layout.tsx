"use client";

import { SettingsSidebar } from "@/components/SettingsSidebar";
import { Sidebar } from "@/components/Sidebar";
import { SidebarLayout } from "@/components/SidebarLayout";
import { api } from "@/lib/api";
import { selfHostedDataSource } from "@/lib/selfHostedDataSource";
import { RouterDashboardDataProvider } from "@/shared/data";

import { usePathname, useRouter } from "next/navigation";

import { useEffect, useState } from "react";

type AuthState = "checking" | "authed" | "anonymous";

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
    return <SidebarLayout sidebar={<div className="md:w-[244px]" />}>{null}</SidebarLayout>;
  }

  const sidebar = pathname.startsWith("/settings") ? <SettingsSidebar /> : <Sidebar />;

  return (
    <RouterDashboardDataProvider dataSource={selfHostedDataSource}>
      <SidebarLayout sidebar={sidebar}>{children}</SidebarLayout>
    </RouterDashboardDataProvider>
  );
}
