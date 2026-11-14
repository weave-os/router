/**
 * Returns an array of numbers. If just the end is provided, values will range from 0 to end. If
 * both start and end are provided, values will range from start to end. If start >= end, the array
 * will be empty.
 */
export function range(end: number): number[];
export function range(start: number, end: number): number[];
export function range(endOrStart: number, maybeEnd?: number): number[] {
  const start = maybeEnd != null ? endOrStart : 0;
  const end = maybeEnd != null ? maybeEnd : endOrStart;

  const length = Math.max(end - start, 0);

  const result = new Array<number>(length);
  for (let i = 0; i < length; i++) {
    result[i] = start + i;
  }

  return result;
}
