/**
 * Puts `values` into a map, keyed the result of `keySelector`.
 *
 * The returned map will have a key for each unique result of `keySelector`, and the value will be
 * the last value that produced that key.
 */
export function keyBy<TKey, TValue>(
  values: Iterable<TValue>,
  keySelector: (value: TValue) => TKey,
): Map<TKey, TValue> {
  const result = new Map<TKey, TValue>();

  for (const value of values) {
    result.set(keySelector(value), value);
  }

  return result;
}
