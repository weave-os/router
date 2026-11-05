"use client";

import { tz } from "@date-fns/tz";
import { type HeaderContext } from "@tanstack/react-table";
import { format, formatDistanceToNowStrict } from "date-fns";

import { type RouterActivityPoint, type RouterIndividualModelMix } from "../../../data";
import { modelColorTokens } from "../../../objects/RouterModelFamily";
import { AccountChartColorToken } from "../../../tools/chartColors";
import { cn } from "../../../tools/cn";
import {
  formatBigDecimal,
  formatPercentage,
  formatRouterModelName,
  formatRouterModelShortName,
} from "../../../tools/format";
import { getRankingColorToken } from "../../../tools/rankingColors";
import { DateFormat } from "../../../tools/time";
import { Avatar } from "../../atoms/Avatar";
import { HighlightedText } from "../../atoms/HighlightedText";
import { Icon } from "../../atoms/Icon";
import { Sparkline } from "../../atoms/Sparkline";
import { Text } from "../../atoms/Text";
import { DataTable } from "../../molecules/DataTable";
import { Tooltip } from "../../molecules/Tooltip";
import { COLUMN_INFO, COLUMN_TITLE, type RouterIndividualsColumn } from "../constants";
import {
  type RouterIndividualRow,
  RouterIndividualRowKind,
  type RouterMetricRank,
  type RouterPassthroughPeriod,
} from "../types";

// Matches the main Individuals table's empty-cell placeholder.
const EMPTY_CELL = "-";

export function RouterIndividualsHeaderCell(props: HeaderContext<RouterIndividualRow, unknown>) {
  const column = props.column.id as RouterIndividualsColumn;
  const title = COLUMN_TITLE[column];
  const info = COLUMN_INFO[column];

  return (
    <DataTable.HeaderCell {...props}>
      <div className="flex min-w-0 flex-1 items-center gap-1">
        <span className="truncate" title={title}>
          {title}
        </span>

        {info != null && (
          <Tooltip
            interactiveChild
            content={<div className="max-w-xs">{info}</div>}
            contentClassName="max-w-xs"
          >
            <span
              role="button"
              tabIndex={-1}
              className="p-1 text-wv-secondary"
              onClick={event => {
                event.preventDefault();
                event.stopPropagation();
              }}
              onKeyDown={event => {
                event.stopPropagation();
              }}
            >
              <Icon type="Info" className="size-4" />
            </span>
          </Tooltip>
        )}
      </div>
    </DataTable.HeaderCell>
  );
}

export function RouterIndividualNameCell({
  row,
  searchQuery,
}: {
  row: RouterIndividualRow;
  searchQuery: string;
}) {
  if (row.kind === RouterIndividualRowKind.Group) {
    return (
      <div className="flex min-w-0 flex-1 items-center gap-2 p-2">
        <Text variant="p" className="truncate text-body font-medium">
          {row.name} <span className="font-normal text-wv-secondary">({row.subRows.length})</span>
        </Text>
      </div>
    );
  }

  return (
    <div className="flex min-w-0 flex-1 items-center gap-2 p-2">
      <Avatar className="size-6 shrink-0" name={row.name} src={row.person.avatarUrl} />
      <Text variant="p" className="truncate text-body font-medium" title={row.name}>
        <HighlightedText query={searchQuery} text={row.name} />
      </Text>
    </div>
  );
}

function TextCell({ children, muted = false }: { children: React.ReactNode; muted?: boolean }) {
  return (
    <div className="flex h-full w-full min-w-0 items-center justify-end px-4">
      <span className={cn("truncate", muted && "text-wv-secondary")}>{children}</span>
    </div>
  );
}

export function RouterLastRequestCell({ lastRequestAt }: { lastRequestAt: Date | null }) {
  if (lastRequestAt == null) return <TextCell muted>{EMPTY_CELL}</TextCell>;
  return (
    <Tooltip content={format(lastRequestAt, DateFormat.DateTimeHumanReadable)} interactiveChild>
      <div className="flex w-full min-w-0 items-center px-4" tabIndex={0}>
        <span className="truncate">
          {formatDistanceToNowStrict(lastRequestAt, { addSuffix: true })}
        </span>
      </div>
    </Tooltip>
  );
}

/** The empty placeholder, with a hover saying why there's no value. */
export function RouterExplainedEmptyCell({ explanation }: { explanation: string }) {
  return (
    <Tooltip
      content={<div className="max-w-xs">{explanation}</div>}
      contentClassName="max-w-xs"
      interactiveChild
    >
      <div className="flex h-full w-full min-w-0 items-center justify-end px-4" tabIndex={0}>
        <span className="text-wv-secondary">{EMPTY_CELL}</span>
      </div>
    </Tooltip>
  );
}

const utc = tz("UTC");

function formatPassthroughPeriod({ end, start }: RouterPassthroughPeriod) {
  const startLabel = format(start, DateFormat.MonthDay, { in: utc });
  return end === start
    ? startLabel
    : `${startLabel} – ${format(end, DateFormat.MonthDay, { in: utc })}`;
}

interface RouterTotalSparkline {
  /** Screen-reader prefix for the per-bucket values, e.g. "Requests per period". */
  label: string;
  points: readonly RouterActivityPoint[];
  /** Shaded behind the line, so it reads which stretches of the trend weren't routed. */
  passthroughBands?: readonly RouterPassthroughPeriod[];
  /** Listed in the tooltip and accessible label, including periods that aren't shaded. */
  passthroughPeriods?: readonly RouterPassthroughPeriod[];
}

/**
 * Right-aligned value with an optional leading sparkline, laid out like the main table's DataCell.
 * A ranked row colors its sparkline by rank, or shows a rank dot when there's no trend to draw.
 */
export function RouterTotalCell({
  formatValue = formatBigDecimal,
  rank,
  sparkline,
  value,
}: {
  formatValue?: (value: number) => string;
  rank?: RouterMetricRank;
  sparkline?: RouterTotalSparkline;
  value: number;
}) {
  if (value === 0) {
    return (
      <div className="flex w-full min-w-0 items-center justify-end px-4">
        <span className="text-wv-secondary">{EMPTY_CELL}</span>
      </div>
    );
  }

  const formatted = formatValue(value);
  const showSparkline = sparkline != null && sparkline.points.length > 1;
  const rankColor = rank == null ? undefined : getRankingColorToken(rank.position, rank.count);
  const rankLabel = rank == null ? null : `rank ${rank.position + 1} of ${rank.count}`;
  const sparklineImage = showSparkline ? (
    <div
      role="img"
      aria-label={[
        `${sparkline.label}: ${sparkline.points.map(point => point.y).join(", ")}`,
        rankLabel,
        ...(sparkline.passthroughPeriods ?? []).map(
          period => `on passthrough ${formatPassthroughPeriod(period)}`,
        ),
      ]
        .filter(part => part != null)
        .join("; ")}
      className="flex h-6 max-w-40 grow text-wv-secondary"
      style={{ color: rankColor }}
    >
      <Sparkline bands={sparkline.passthroughBands} data={sparkline.points} />
    </div>
  ) : null;
  return (
    <div className="flex w-full min-w-0 items-center justify-end px-4">
      <div className="mr-auto flex min-w-0 flex-grow items-center justify-start gap-2 pr-2">
        {showSparkline ? (
          (sparkline.passthroughPeriods ?? []).length > 0 ? (
            <Tooltip
              content={
                <div className="flex flex-col gap-1">
                  <span>On passthrough (requests went to the model asked for, not routed):</span>
                  <ul className="list-disc pl-4">
                    {(sparkline.passthroughPeriods ?? []).map(period => (
                      <li key={period.start}>{formatPassthroughPeriod(period)}</li>
                    ))}
                  </ul>
                </div>
              }
              contentClassName="max-w-64"
              interactiveChild
            >
              {sparklineImage}
            </Tooltip>
          ) : (
            sparklineImage
          )
        ) : (
          rankLabel != null && (
            <div
              role="img"
              aria-label={rankLabel}
              className="size-1.5 shrink-0 rounded-full"
              style={{ backgroundColor: rankColor }}
              title={rankLabel}
            />
          )
        )}
      </div>
      {/* A fixed minimum value width keeps sparklines equally wide across rows; otherwise a
          short value like "23/w" lets its sparkline grow past its neighbors'. */}
      <span
        className={cn("truncate tabular-nums", showSparkline && "min-w-[8ch] shrink-0 text-right")}
        title={formatted}
      >
        {formatted}
      </span>
    </div>
  );
}

// The neutral grey the cost-by-fidelity bar uses for its catch-all slice.
const OTHER_MODELS_COLOR = AccountChartColorToken.Gray;
const OTHER_MODELS_KEY = "other";

export interface RouterModelMixSegment {
  color: string;
  key: string;
  label: string;
  requests: number;
  /** Fraction of the person's routed requests, from 0 to 1. */
  share: number;
}

export function modelMixSegments(modelMix: RouterIndividualModelMix): RouterModelMixSegment[] {
  const totalRequests =
    modelMix.models.reduce((sum, { requests }) => sum + requests, 0) + modelMix.otherRequests;
  if (totalRequests === 0) return [];
  const colors = modelColorTokens(modelMix.models.map(({ model }) => model));
  const segments = modelMix.models.map(({ model, requests }, rank) => ({
    color: colors[rank],
    key: `model:${model}`,
    label: model,
    requests,
    share: requests / totalRequests,
  }));
  if (modelMix.otherRequests > 0) {
    segments.push({
      color: OTHER_MODELS_COLOR,
      key: OTHER_MODELS_KEY,
      label: "Other models",
      requests: modelMix.otherRequests,
      share: modelMix.otherRequests / totalRequests,
    });
  }
  return segments;
}

export function RouterModelMixCell({ modelMix }: { modelMix: RouterIndividualModelMix | null }) {
  const segments = modelMix == null ? [] : modelMixSegments(modelMix);
  if (segments.length === 0) return <TextCell muted>{EMPTY_CELL}</TextCell>;
  return (
    <div className="flex w-full min-w-0 items-center gap-4 px-4">
      <div
        role="img"
        aria-label={`Routed models: ${segments
          .map(
            segment => `${formatRouterModelName(segment.label)} ${formatPercentage(segment.share)}`,
          )
          .join(", ")}`}
        className="relative flex h-3 w-24 shrink-0 gap-px overflow-hidden rounded"
      >
        <div
          aria-hidden="true"
          className="absolute inset-y-0 right-0 bg-wv-track"
          style={{ left: `${segments.reduce((sum, segment) => sum + segment.share, 0) * 100}%` }}
        />
        {segments
          .filter(segment => segment.share > 0)
          .map(segment => (
            <div
              key={segment.key}
              className="h-full min-w-[2px]"
              style={{ backgroundColor: segment.color, width: `${segment.share * 100}%` }}
            />
          ))}
      </div>
      <span className="min-w-0 flex-1 truncate text-start text-caption text-wv-secondary">
        {formatRouterModelShortName(formatRouterModelName(segments[0].label))}
      </span>
    </div>
  );
}

export function RouterModelMixLegend({ modelMix }: { modelMix: RouterIndividualModelMix }) {
  return (
    <dl className="grid grid-cols-[auto_auto_auto] items-center gap-x-3 gap-y-1 text-caption">
      {modelMixSegments(modelMix).map(segment => (
        <div key={segment.key} className="contents">
          <dt className="flex min-w-0 items-center gap-2">
            <span
              aria-hidden="true"
              className="size-2 shrink-0 rounded-sm"
              style={{ backgroundColor: segment.color }}
            />
            {formatRouterModelName(segment.label)}
          </dt>
          <dd className="text-right tabular-nums">{formatPercentage(segment.share)}</dd>
          <dd className="text-right tabular-nums text-wv-secondary">
            {formatBigDecimal(segment.requests)} requests
          </dd>
        </div>
      ))}
    </dl>
  );
}
