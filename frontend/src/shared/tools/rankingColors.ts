import chroma from "chroma-js";

import { RankingChartColor, RankingChartColorToken } from "./chartColors";

/**
 * Color scale for ranking visualization
 * Colors from orange (worst) to yellow (middle) to green (best)
 */
const scale = chroma
  .scale([RankingChartColor.Worst, RankingChartColor.Middle, RankingChartColor.Best])
  .mode("lab");

/**
 * Gets a color for ranking visualization based on position and total count.
 * Position 0 is the best (highest value), so it gets the greenest color.
 *
 * @param position - 0-based position in the sorted list (0 = best)
 * @param totalCount - Total number of items
 * @returns Hex color string, or undefined if no meaningful ranking (totalCount <= 1)
 */
export function getRankingColor(position: number, totalCount: number): string | undefined {
  if (totalCount <= 1) return undefined;

  // Position 0 is best, so invert the calculation
  const normalized = (totalCount - position - 1) / (totalCount - 1);
  return scale(normalized).hex();
}

// Only exact stops use tokens; interpolated ranks retain Chroma's concrete Lab rounding.
export function getRankingColorToken(position: number, totalCount: number): string | undefined {
  if (totalCount <= 1) return undefined;
  const normalized = (totalCount - position - 1) / (totalCount - 1);
  if (normalized >= 1) return RankingChartColorToken.Best;
  if (normalized <= 0) return RankingChartColorToken.Worst;
  if (normalized === 0.5) return RankingChartColorToken.Middle;
  return getRankingColor(position, totalCount);
}
