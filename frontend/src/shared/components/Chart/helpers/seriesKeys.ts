const PARTIAL_BUCKET_SERIES_PREFIX = "__partial__";

export function getPartialBucketSeriesKey(series: string): string {
  return `${PARTIAL_BUCKET_SERIES_PREFIX}${series}`;
}

export function isPartialBucketSeriesKey(key: string): boolean {
  return key.startsWith(PARTIAL_BUCKET_SERIES_PREFIX);
}

export function getOriginalFromPartialBucketSeriesKey(key: string): string {
  if (!isPartialBucketSeriesKey(key)) return key;
  return key.slice(PARTIAL_BUCKET_SERIES_PREFIX.length);
}

const PROJECTED_SERIES_PREFIX = "__proj__";

/**
 * Gets the key for the projected version of the given series.
 */
export function getProjectedSeriesKey(series: string) {
  return `${PROJECTED_SERIES_PREFIX}${series}`;
}

/**
 * Checks if the given key is a projected series key.
 */
export function isProjectedSeriesKey(key: string) {
  return key.startsWith(PROJECTED_SERIES_PREFIX);
}
