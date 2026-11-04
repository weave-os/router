const PARTIAL_BUCKET_SERIES_PREFIX = "__partial__";

export function getPartialBucketSeriesKey(series: string): string {
  return `${PARTIAL_BUCKET_SERIES_PREFIX}${series}`;
}

export function isPartialBucketSeriesKey(key: string): boolean {
  return key.startsWith(PARTIAL_BUCKET_SERIES_PREFIX);
}

const PROJECTED_SERIES_PREFIX = "__proj__";

export function getProjectedSeriesKey(series: string) {
  return `${PROJECTED_SERIES_PREFIX}${series}`;
}

export function isProjectedSeriesKey(key: string) {
  return key.startsWith(PROJECTED_SERIES_PREFIX);
}
