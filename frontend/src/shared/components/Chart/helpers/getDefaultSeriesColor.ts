import {
  ACCOUNT_SERIES_COLORS,
  ACCOUNT_SERIES_COLOR_TOKENS,
  ChartColorToken,
  PIE_SERIES_COLORS,
  PIE_SERIES_COLOR_TOKENS,
  SERIES_COLORS,
  SERIES_COLOR_TOKENS,
} from "../../../tools/chartColors";
import { hashString } from "../../../tools/hash";

export { ACCOUNT_SERIES_COLORS, ACCOUNT_SERIES_COLOR_TOKENS, SERIES_COLORS, SERIES_COLOR_TOKENS };

/**
 * Deterministic FNV-1a hash used to map a stable string to an index in a palette.
 * Mirrors the project-id hashing in useProjectSeriesConfigs so colors stay consistent
 * across chart surfaces even when the rendered subset changes.
 */
export function hashStringToIndex(s: string, modulus: number): number {
  // Preserve signed palette slotting so existing chart colors do not change.
  return Math.abs(hashString(s)) % modulus;
}

function getPaletteColor<Color extends string>(colors: readonly Color[], seriesIndex: number) {
  return colors[seriesIndex % colors.length];
}

export function getDefaultSeriesColor(seriesIndex: number): string {
  return getPaletteColor(SERIES_COLORS, seriesIndex);
}

export function getDefaultSeriesColorToken(seriesIndex: number): ChartColorToken {
  return getPaletteColor(SERIES_COLOR_TOKENS, seriesIndex);
}

export function getDefaultPieChartSeriesColor(seriesIndex: number): string {
  return getPaletteColor(PIE_SERIES_COLORS, seriesIndex);
}

export function getDefaultPieChartSeriesColorToken(seriesIndex: number): ChartColorToken {
  return getPaletteColor(PIE_SERIES_COLOR_TOKENS, seriesIndex);
}
