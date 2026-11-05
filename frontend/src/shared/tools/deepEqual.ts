import equal from "deep-equal";

/**
 * Checks if two values are deeply equal.
 */
export function deepEqual<A>(a: A, b: unknown): b is A {
  return equal(a, b);
}
