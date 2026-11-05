/**
 * Gets the values with the given `keys` from `map`. The values returned will be in the same order
 * as the keys in `keys`, with any missing keys omitted.
 */
export function getDefinedValues<K, V>(map: ReadonlyMap<K, V>, keys: Iterable<K>): V[] {
  const result: V[] = [];

  for (const key of keys) {
    if (map.has(key)) {
      result.push(map.get(key)!);
    }
  }

  return result;
}
