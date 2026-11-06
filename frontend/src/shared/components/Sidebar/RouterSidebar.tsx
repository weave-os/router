"use client";

import { Icon, type IconType } from "@weave-os/ui/components/atoms/Icon";
import { Button } from "@weave-os/ui/components/molecules/Button";
import { Appearance } from "@weave-os/ui/components/types";

import { usePathname } from "next/navigation";

import { SidebarNavButton } from "./SidebarNavButton";
import { SidebarNavIcon } from "./SidebarNavIcon";

export interface RouterSidebarNavItem {
  href: string;
  icon: IconType;
  label: string;
}

export interface RouterSidebarUtilityItem {
  href?: string;
  icon: IconType;
  label: string;
  newTab?: boolean;
  onClick?: () => void;
}

export interface RouterSidebarProps {
  accountMenu: React.ReactNode;
  footer?: React.ReactNode;
  logo: React.ReactNode;
  navItems: readonly RouterSidebarNavItem[];
  utilityItems: readonly RouterSidebarUtilityItem[];
}

function isNavItemSelected(href: string, path: string) {
  return path === href || path.startsWith(`${href}/`);
}

function NavButton({ item }: { item: RouterSidebarNavItem }) {
  const selected = isNavItemSelected(item.href, usePathname());

  return (
    <SidebarNavButton
      href={item.href}
      className="justify-center sm:justify-start"
      aria-selected={selected}
      aria-current={selected ? "page" : undefined}
      title={item.label}
    >
      <div className="relative flex items-center gap-2 sm:w-full">
        <SidebarNavIcon type={item.icon} active={selected} />
        <span className="hidden whitespace-nowrap text-body sm:inline">{item.label}</span>
      </div>
    </SidebarNavButton>
  );
}

// Below sm the sidebar is an icon rail, so utility buttons need larger tap targets.
function UtilityButton({ href, icon, label, newTab = false, onClick }: RouterSidebarUtilityItem) {
  return (
    <Button
      href={href}
      newTab={newTab}
      onClick={onClick}
      appearance={Appearance.Hollow}
      className="h-11 w-full justify-center gap-2 rounded px-2 text-caption font-normal sm:h-8 sm:justify-start"
      title={label}
    >
      <Icon type={icon} className="size-4 shrink-0 text-wv-secondary sm:size-3" />
      <span className="hidden sm:inline">{label}</span>
      {newTab && (
        <Icon type="ArrowUpRight" className="hidden size-3.5 text-wv-secondary sm:inline" />
      )}
    </Button>
  );
}

export function RouterSidebar({
  accountMenu,
  footer,
  logo,
  navItems,
  utilityItems,
}: RouterSidebarProps) {
  return (
    <div className="surface-navigation flex h-full w-full flex-col items-start overflow-y-auto">
      <header className="flex w-full flex-col items-center gap-4 py-2 sm:flex-row sm:px-2 sm:pt-4">
        {logo}
        <div className="sm:ml-auto">{accountMenu}</div>
      </header>

      <hr className="w-full border-t border-border-darker sm:hidden" />

      <nav className="flex w-full flex-col gap-4 sm:px-2 sm:py-1">
        <div className="flex w-full flex-col gap-1">
          {navItems.map(item => (
            <NavButton key={item.href} item={item} />
          ))}
        </div>
      </nav>

      <div className="mt-auto flex w-full flex-col gap-2 p-2">
        <div className="flex w-full flex-col gap-1">
          {utilityItems.map(item => (
            <UtilityButton key={item.label} {...item} />
          ))}
        </div>

        {footer}
      </div>
    </div>
  );
}
