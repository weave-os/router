/* oxlint-disable typescript/no-explicit-any */

/**
 * Updates the given `key` in `map` with the value returned by `updater`, mutating `map` and
 * returning it.
 */
export function updateKey<
  TMap extends Map<any, any>,
  TKey extends (TMap extends Map<infer K, any> ? K : never),
>(
  map: TMap,
  key: TKey,
  updater: (
    value: TMap extends Map<any, infer V> ? V | undefined : never,
  ) => TMap extends Map<any, infer V> ? V : never,
): TMap {
  map.set(key, updater(map.get(key)));
  return map;
}
