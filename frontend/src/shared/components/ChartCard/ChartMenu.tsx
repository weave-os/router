"use client";

import { useState } from "react";

import { copyToClipboard } from "../../tools/clipboard";
import { buildShareUrl, URLParam } from "../../tools/shareUrl";
import { Icon } from "../atoms/Icon";
import { Text } from "../atoms/Text";
import { Button } from "../molecules/Button";
import { Popover } from "../molecules/Popover";
import { Appearance } from "../types";

export interface ChartMenuProps {
  /** Stable chart id included in the shared URL as `?chart=<chartId>`. */
  chartId: string;
  /**
   * Optional "Explain with AI" menu row, rendered between the "Copy link"
   * item and any extra items. Callers are responsible for styling this as a
   * full-width `<Button appearance={Hollow} size="sm" className="w-full justify-start">`
   * so it matches the surrounding rows (see {@link ExplainChartButton}).
   */
  explainAction?: React.ReactNode;
  /**
   * Additional menu items rendered after the built-in "Copy link" item. Items
   * should match the surrounding `Button` style.
   */
  extraItems?: React.ReactNode;
  /**
   * Optional chart-specific settings body. When provided, the main view shows
   * a "Chart settings →" row that navigates to a sub-view with a back arrow
   * and this content. Mirrors the navigation pattern used by
   * {@link DashboardSettingsButton}.
   */
  settingsContent?: React.ReactNode;
}

type View = "main" | "settings";

/**
 * Overflow menu for a chart, rendered as an always-visible ellipsis button in
 * the header. The first item copies a deep-link URL that preserves the current
 * page state. Callers can extend the menu with {@link ChartMenuProps.explainAction},
 * {@link ChartMenuProps.extraItems}, and {@link ChartMenuProps.settingsContent}.
 *
 * When `settingsContent` is provided, clicking "Chart settings" swaps the
 * popover to a sub-view with a back arrow and the settings body — the same
 * pattern used by {@link DashboardSettingsButton} in the page header.
 */
export function ChartMenu({ chartId, explainAction, extraItems, settingsContent }: ChartMenuProps) {
  const [isOpen, setIsOpen] = useState(false);
  const [view, setView] = useState<View>("main");

  const setOpen = (open: boolean) => {
    setIsOpen(open);
    // Reset back to the main view whenever the popover closes so the next open
    // always starts on the top-level list.
    if (!open) setView("main");
  };

  const hasSettings = settingsContent != null;

  return (
    <Popover open={isOpen} onOpenChange={setOpen}>
      <Popover.Trigger>
        <Button
          size="icon"
          appearance={Appearance.Hollow}
          className="shrink-0"
          aria-label="Chart options"
        >
          <Icon type="Ellipsis" />
        </Button>
      </Popover.Trigger>

      <Popover.Content
        className={
          view === "settings" ? "flex w-72 flex-col gap-4 p-3" : "flex w-52 flex-col gap-1 p-1"
        }
        align="end"
      >
        {view === "main" ? (
          <>
            <Button
              size="sm"
              appearance={Appearance.Hollow}
              className="w-full justify-start"
              onClick={() => {
                void copyToClipboard(buildShareUrl(URLParam.Chart, chartId));
                setOpen(false);
              }}
            >
              <Icon type="Link" />
              Copy link to chart
            </Button>
            {explainAction != null && (
              // Dismiss the menu on any bubble-up click from the action row so
              // callers don't have to thread a close callback through.
              // `display: contents` keeps this wrapper invisible to the
              // surrounding flex column. Disabled buttons don't emit clicks,
              // so a disabled action (e.g., while sending) won't close the
              // menu spuriously.
              <div className="contents" onClick={() => setOpen(false)}>
                {explainAction}
              </div>
            )}
            {extraItems}
            {hasSettings && (
              <Button
                size="sm"
                appearance={Appearance.Hollow}
                className="w-full justify-between"
                onClick={() => setView("settings")}
              >
                <span className="flex items-center gap-2">
                  <Icon type="Settings" />
                  Chart settings
                </span>
                <Icon type="ArrowRight" size="sm" />
              </Button>
            )}
          </>
        ) : (
          <>
            <div className="flex items-center gap-2">
              <Button
                onClick={() => setView("main")}
                appearance={Appearance.Hollow}
                size="icon"
                aria-label="Back to chart options"
              >
                <Icon type="ArrowLeft" />
              </Button>
              <Text variant="p" className="text-body font-semibold">
                Chart settings
              </Text>
            </div>
            {settingsContent}
          </>
        )}
      </Popover.Content>
    </Popover>
  );
}
