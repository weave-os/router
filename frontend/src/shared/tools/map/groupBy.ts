import { updateKey } from "./updateKey";

/**
 * Groups `values` by the result of `keySelector`.
 *
 * The returned map will have a key for each unique result of `keySelector`, and the value will be
 * an array of all the values that produced that key. The result will never contain empty arrays.
 */
export function groupBy<TKey, TValue>(
  values: Iterable<TValue>,
  keySelector: (value: TValue) => TKey,
): Map<TKey, TValue[]> {
  const result = new Map<TKey, TValue[]>();

  for (const value of values) {
    updateKey(result, keySelector(value), (grouped = []) => {
      grouped.push(value);
      return grouped;
    });
  }

  return result;
}
