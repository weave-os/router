import { KeyRound, Plug, SlidersHorizontal } from "lucide-react";
import { type ReactNode } from "react";

export interface SettingsNavItem {
  href: string;
  label: string;
  icon: ReactNode;
}

/**
 * The settings tabs, in nav order. Mirrors the hosted dashboard's settings
 * shell: one route per section, driven by a single label/icon config. Billing
 * and uninstall are hosted-only, so self-hosted stops at three tabs.
 */
export const SETTINGS_NAV: SettingsNavItem[] = [
  { href: "/settings", label: "General", icon: <KeyRound size={16} /> },
  { href: "/settings/providers", label: "Provider keys", icon: <Plug size={16} /> },
  { href: "/settings/models", label: "Models & routing", icon: <SlidersHorizontal size={16} /> },
];

export function settingsNavItem(href: string): SettingsNavItem {
  const item = SETTINGS_NAV.find(i => i.href === href);
  if (item == null) throw new Error(`unknown settings route: ${href}`);
  return item;
}
