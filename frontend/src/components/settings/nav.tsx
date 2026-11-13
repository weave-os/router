import { type IconType } from "@/shared/components/atoms/Icon";

export interface SettingsNavItem {
  href: string;
  label: string;
  icon: IconType;
}

/**
 * The settings tabs, in nav order. Mirrors the hosted dashboard's settings
 * shell: one route per section, driven by a single label/icon config. Billing
 * and uninstall are hosted-only.
 */
export const SETTINGS_NAV: SettingsNavItem[] = [
  { href: "/settings", label: "General", icon: "Key" },
  { href: "/settings/providers", label: "Provider keys", icon: "Server" },
  { href: "/settings/models", label: "Models & routing", icon: "SlidersHorizontal" },
  { href: "/settings/subscriptions", label: "Subscriptions", icon: "Link" },
];

export function settingsNavItem(href: string): SettingsNavItem {
  const item = SETTINGS_NAV.find(i => i.href === href);
  if (item == null) throw new Error(`unknown settings route: ${href}`);
  return item;
}
