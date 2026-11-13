"use client";

import React, { useEffect, useMemo, useState } from "react";

import { cn } from "../../tools/cn";
import { Text } from "../atoms/Text";
import { Card } from "../molecules/Card";
import { useChartDashboardLayout } from "./ChartDashboardLayoutContext";
import { ChartMenu } from "./ChartMenu";
import { ChartHeaderLayoutContext } from "./ChartViewTray";

/**
 * The chart card title row, for cards that build their own header but should read as chart cards.
 * Pair with `Card.Title variant="h4"`, or with `text-card-title` on a plain element.
 */
export const CHART_CARD_TITLE_CLASS =
  "flex min-h-6 min-w-0 max-w-full items-center gap-3 font-[number:var(--wv-card-title-weight)] leading-tight";

/**
 * Lets 32px controls inside a chart title (dropdown triggers, help buttons) hang over the 24px
 * title line instead of growing the header.
 */
const CHART_CARD_TITLE_CONTROLS_CLASS = "[&_button]:-my-1";

export interface ChartCardProps extends React.PropsWithChildren {
  /**
   * If true, the content area uses auto height instead of the fixed h-80.
   * Use for stat cards and tables that don't need a fixed chart height.
   *
   * @default false
   */
  autoHeight?: boolean;
  /**
   * Stable id used for deep linking. When provided, the card is marked with a
   * `data-chart-id` attribute and an ellipsis menu (with "Copy link to chart")
   * is rendered in the top-right action row.
   */
  chartId?: string;
  className?: string;
  /**
   * Optional "Explain with AI" action, rendered as a row inside the kebab
   * menu. Only used when {@link chartId} is provided.
   */
  explainAction?: React.ReactNode;
  /**
   * If provided, renders full-width content inside the card beneath the chart (e.g. a
   * colour legend). Kept inside the card so grid parents don't treat it as a sibling cell.
   */
  footer?: React.ReactNode;
  /**
   * If true, drops the card's padding and fill so the content sits flush on the page.
   *
   * @default false
   */
  fullBleed?: boolean;
  /**
   * If provided, renders full-width content below the title/topRight row within the header.
   */
  headerBottom?: React.ReactNode;
  /**
   * The chart's headline figure (value, unit, change), anchored under the title on the left.
   * Keep numbers here, not in {@link topRight}, which is for controls.
   */
  headline?: React.ReactNode;
  /**
   * Additional items to render in the kebab menu (after the built-in "Copy
   * link" item). Only rendered when {@link chartId} is provided.
   */
  menuExtraItems?: React.ReactNode;
  /**
   * Slot to render an optional overlay over the chart.
   */
  overlay?: React.ReactNode;
  /**
   * Optional chart-specific settings body (e.g., previous-period toggle +
   * goal-line editor). When provided, the kebab menu adds a "Chart settings"
   * row that navigates to a sub-view containing this content. Only used when
   * {@link chartId} is provided.
   */
  settingsContent?: React.ReactNode;
  /**
   * If provided, renders the sources for the metric next to the title.
   */
  sources?: React.ReactNode;
  /**
   * The optional subtitle to show in the header.
   */
  subtitle?: React.ReactNode;
  /**
   * The chart's title.
   */
  title: React.ReactNode;
  /**
   * Optional element rendered immediately after the title (e.g. an info tooltip icon).
   * Unlike {@link sources}, it stays visible at every container width and does not participate
   * in title truncation.
   */
  titleAccessory?: React.ReactNode;
  /**
   * Controls rendered in the top right corner of the card, on the title's line. They hang over
   * the line rather than growing it, so the header stays as tall as the title.
   */
  topRight?: React.ReactNode;
  /**
   * Optional content rendered at the LEFT edge of the top-right action cluster, before the kebab
   * and {@link topRight}. Use for a persistent affordance such as a "Tips" badge.
   */
  topRightLeading?: React.ReactNode;
  /**
   * If true, renders the title as a single-line truncating element with
   * ellipsis + full-text hover tooltip. Set to false only when rendering a
   * composite title block (e.g. a dropdown or multi-row title) that manages
   * its own layout — the caller is then responsible for any needed
   * truncation inside that block.
   *
   * @default true
   */
  truncateTitle?: boolean;
}

/**
 * Renders a card that contains a chart, with some common styling. Does not have any opinions about
 * the chart itself.
 *
 * Chart-level actions ("Explain with AI", "Chart settings") live inside the
 * kebab menu (see {@link ChartMenu}), which sits to the left of the `topRight` slot.
 */
export function ChartCard({
  autoHeight = false,
  chartId,
  children,
  className,
  explainAction,
  footer,
  fullBleed = false,
  headerBottom,
  headline,
  menuExtraItems,
  overlay,
  settingsContent,
  sources,
  subtitle,
  title,
  titleAccessory,
  topRight,
  topRightLeading,
  truncateTitle = true,
}: ChartCardProps) {
  const { fillHeight } = useChartDashboardLayout();
  const [headerElement, setHeaderElement] = useState<HTMLDivElement | null>(null);
  const [titleLineSlot, setTitleLineSlot] = useState<HTMLDivElement | null>(null);
  const [availableWidth, setAvailableWidth] = useState(0);

  useEffect(() => {
    // Without ResizeObserver (jsdom) the width stays 0 and a view tray stays under the headline.
    if (headerElement == null || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(([entry]) => setAvailableWidth(entry.contentRect.width));
    observer.observe(headerElement);
    return () => observer.disconnect();
  }, [headerElement]);

  const headerLayout = useMemo(
    () => ({ availableWidth, titleLineSlot }),
    [availableWidth, titleLineSlot],
  );

  // Only clamp/clip plain string titles. Interactive or composite element
  // titles (e.g. dropdown selectors) manage their own layout and must not be
  // clipped, otherwise their hover highlight's rounded corners get cut off.
  const clampTitle = truncateTitle && typeof title === "string";

  const contentHeightClass = fillHeight ? "h-full" : autoHeight ? "h-auto" : "h-80";

  return (
    <Card
      className={cn(
        "relative h-full w-full shrink grow @container",
        // Inheriting the surface keeps descendants that paint `bg-wv-surface` (e.g. the chart's
        // loading overlay) matched to the page instead of the card layer. Dark tabs trays still
        // paint the card's inset, which equals the workspace behind, so they need the outline.
        fullBleed
          ? "border-0 bg-transparent p-0 [--wv-bg-surface:inherit] dark:[--wv-tabs-tray-border:var(--wv-border-default)]"
          : "p-4 md:p-5",
        "gap-y-5",
        className,
      )}
      data-chart-id={chartId}
    >
      <Card.Header ref={setHeaderElement} className="gap-3">
        <div className="group/chart-header flex flex-row flex-wrap items-start gap-x-4 gap-y-1">
          <div className="flex min-w-0 grow basis-0 flex-col gap-1">
            <Card.Title
              variant="h4"
              className={cn(CHART_CARD_TITLE_CLASS, CHART_CARD_TITLE_CONTROLS_CLASS, "grow")}
            >
              {clampTitle ? (
                <span className="min-w-0 truncate" title={title}>
                  {title}
                </span>
              ) : (
                title
              )}
              {titleAccessory}
              {sources != null && <div className="hidden shrink-0 @sm:block">{sources}</div>}
            </Card.Title>

            {headline != null && (
              // Collapses when the headline renders nothing (e.g. an errored statistic), so no gap is left.
              <div className="mt-2 empty:hidden has-[>:only-child:empty]:hidden">{headline}</div>
            )}

            {React.Children.count(subtitle) > 0 && (
              <Text
                variant="p"
                className="mt-1 line-clamp-2 break-words text-body text-wv-secondary"
                title={typeof subtitle === "string" ? subtitle : undefined}
              >
                {subtitle}
              </Text>
            )}
          </div>

          <div className="-my-1 ml-auto flex max-w-full shrink-0 items-center gap-2">
            <div ref={setTitleLineSlot} className="flex items-center empty:hidden" />
            {topRightLeading != null && (
              // items-center keeps it aligned with the kebab row rather than the header's top edge.
              <div className="flex items-center self-center">{topRightLeading}</div>
            )}
            {chartId != null && (
              <ChartMenu
                chartId={chartId}
                explainAction={explainAction}
                extraItems={menuExtraItems}
                settingsContent={settingsContent}
              />
            )}
            {topRight}
          </div>
        </div>

        <ChartHeaderLayoutContext value={headerLayout}>{headerBottom}</ChartHeaderLayoutContext>
      </Card.Header>

      {/* The 4px clip margin keeps the chart's focus ring visible without moving the chart. */}
      <Card.Content
        className={cn(
          "relative -m-1 box-content min-h-0 grow overflow-hidden p-1",
          contentHeightClass,
        )}
      >
        {children}
      </Card.Content>

      {footer != null && <Card.Footer className="p-0">{footer}</Card.Footer>}

      {overlay}
    </Card>
  );
}
