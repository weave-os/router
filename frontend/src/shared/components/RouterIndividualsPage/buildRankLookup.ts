/**
 * Builds an O(1) rank lookup for values sorted in ascending order.
 *
 * Ties preserve the table's existing competition-ranking behavior: lower-is-better values use
 * their first occurrence, while higher-is-better values use their last occurrence.
 */
export function buildRankLookup(
  sortedValues: readonly number[],
  lowerIsBetter: boolean,
): ReadonlyMap<number, number> {
  const ranks = new Map<number, number>();

  for (let index = 0; index < sortedValues.length; index++) {
    const value = sortedValues[index];
    if (Number.isNaN(value)) {
      // Preserve findIndex/findLastIndex behavior from the previous implementation.
      if (!lowerIsBetter) ranks.set(value, sortedValues.length + 1);
      continue;
    }

    if (lowerIsBetter) {
      if (!ranks.has(value)) ranks.set(value, index + 1);
    } else {
      // Overwrite ties so the last occurrence determines the descending rank.
      ranks.set(value, sortedValues.length - index);
    }
  }

  return ranks;
}
