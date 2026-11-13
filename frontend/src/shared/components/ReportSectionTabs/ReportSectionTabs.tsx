"use client";

import Link from "next/link";

import { cn } from "../../tools/cn";

export interface ReportSectionTabItem {
  href: string;
  isActive: boolean;
  label: string;
}

export interface ReportSectionTabsProps {
  /** Accessible name for the tab list. Defaults to report-section copy. */
  ariaLabel?: string;
  items: readonly ReportSectionTabItem[];
}

/**
 * Horizontal underline tabs for report section navigation.
 * Spacing matches the SF design; colors and typography use the app design system.
 */
export function ReportSectionTabs({
  ariaLabel = "Report sections",
  items,
}: ReportSectionTabsProps) {
  if (items.length === 0) {
    return null;
  }

  return (
    <div className="flex w-full justify-center border-b border-wv-subtle">
      <nav
        aria-label={ariaLabel}
        className="flex w-full max-w-content-width gap-1.5 overflow-x-auto px-6"
      >
        {items.map(({ href, isActive, label }) => (
          <Link
            key={href}
            href={href}
            aria-current={isActive ? "page" : undefined}
            onKeyDown={event => {
              if (event.key !== " ") return;
              event.preventDefault();
              event.currentTarget.click();
            }}
            className={cn(
              "shrink-0 whitespace-nowrap rounded-t-sm border-b-2 px-2 pb-2 pt-1.5 text-body font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-wv-focus",
              isActive
                ? "border-brand text-wv-primary"
                : "border-transparent text-wv-secondary hover:text-wv-primary",
            )}
          >
            {label}
          </Link>
        ))}
      </nav>
    </div>
  );
}
