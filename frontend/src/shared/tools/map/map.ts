/**
 * Maps over a map, transforming each value.
 */
export function map<K, V, U>(map: ReadonlyMap<K, V>, fn: (value: V, key: K) => U): Map<K, U> {
  const result = new Map<K, U>();

  for (const [key, value] of map.entries()) {
    result.set(key, fn(value, key));
  }

  return result;
}
