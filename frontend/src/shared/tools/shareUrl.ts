export enum URLParam {
  Chart = "chart",
}

/**
 * Builds a link to the current page with `param` set, so a copied link reopens the same view.
 */
export function buildShareUrl(param: string, value: string): string {
  const url = new URL(window.location.href);
  url.searchParams.set(param, value);
  return url.toString();
}
