"use client";

import { usePathname } from "next/navigation";

import { Icon, type IconType } from "../atoms/Icon";
import { Text } from "../atoms/Text";
import { SidebarNavButton } from "./SidebarNavButton";
import { SidebarNavIcon } from "./SidebarNavIcon";

export interface RouterSettingsNavItem {
  href: string;
  icon: IconType;
  label: string;
}

export interface RouterSettingsNavGroup {
  items: readonly RouterSettingsNavItem[];
  label: string;
}

export interface RouterSettingsSidebarProps {
  accountMenu: React.ReactNode;
  dashboardHref: string;
  groups: readonly RouterSettingsNavGroup[];
  logo: React.ReactNode;
  /** Routes that only match exactly, e.g. an index tab that nested tabs live under. */
  exactMatchHrefs?: readonly string[];
}

/** Takes the place of `RouterSidebar` under the settings routes. */
export function RouterSettingsSidebar({
  accountMenu,
  dashboardHref,
  exactMatchHrefs = [],
  groups,
  logo,
}: RouterSettingsSidebarProps) {
  const pathname = usePathname();
  const isSelected = (href: string) =>
    pathname === href || (!exactMatchHrefs.includes(href) && pathname.startsWith(`${href}/`));

  return (
    <div className="surface-navigation flex h-full w-full flex-col items-start overflow-y-auto">
      <header className="flex w-full flex-col items-center gap-4 py-2 sm:flex-row sm:px-2 sm:pt-4">
        {logo}
        <div className="sm:ml-auto">{accountMenu}</div>
      </header>

      <hr className="w-full border-t border-wv-subtle sm:hidden" />

      <div className="flex w-full flex-col gap-1 sm:px-2 sm:py-1">
        <SidebarNavButton
          href={dashboardHref}
          className="justify-center sm:justify-start"
          title="Back to dashboard"
        >
          <div className="flex items-center gap-2 text-wv-secondary sm:w-full">
            <Icon type="ChevronLeft" className="size-3 shrink-0" />
            <span className="hidden whitespace-nowrap text-body sm:inline">Back to dashboard</span>
          </div>
        </SidebarNavButton>

        <hr className="my-2 hidden w-full border-t border-wv-subtle sm:block" />
      </div>

      <nav aria-label="Settings" className="flex w-full flex-col gap-4 pb-2 sm:px-2">
        {groups.map(group => (
          <div key={group.label} className="flex flex-col gap-1">
            <Text className="hidden px-2.5 text-caption text-wv-secondary sm:block">
              {group.label}
            </Text>
            <ul aria-label={group.label} className="flex flex-col gap-1">
              {group.items.map(item => {
                const selected = isSelected(item.href);
                return (
                  <li key={item.href} className="flex">
                    <SidebarNavButton
                      href={item.href}
                      className="justify-center text-wv-primary sm:justify-start"
                      aria-current={selected ? "page" : undefined}
                      title={item.label}
                    >
                      <div className="flex items-center gap-2 sm:w-full">
                        <SidebarNavIcon type={item.icon} active={selected} />
                        <span className="hidden whitespace-nowrap text-body sm:inline">
                          {item.label}
                        </span>
                      </div>
                    </SidebarNavButton>
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </nav>
    </div>
  );
}
