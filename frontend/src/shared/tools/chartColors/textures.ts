/**
 * Dot texture: one dot per 4px tile, colored by `--color-bg`. Marks a fill that is a different
 * kind of thing from its neighbors, e.g. "Other models" or a passthrough period.
 * Keep in sync with `DOT_TEXTURE_TILE` and `DOT_TEXTURE_RADIUS`, its SVG `<pattern>` form.
 */
export const DOT_TEXTURE_CLASS_NAME =
  "bg-[radial-gradient(circle,var(--color-bg)_0.8px,transparent_1px)] [background-size:4px_4px] [background-position:-1px_-1px]";

export const DOT_TEXTURE_TILE = 4;
export const DOT_TEXTURE_RADIUS = 0.8;

/**
 * Hatch texture: full-color stripes alternating with 40%-alpha stripes of `--color-bg`. Marks a
 * projected or not-yet-incurred amount beside its solid actual. Matches the bars'
 * `diagonalLinesMask`.
 */
export const HATCHED_TEXTURE_CLASS_NAME =
  "bg-[repeating-linear-gradient(135deg,var(--color-bg)_0_3px,color-mix(in_srgb,var(--color-bg)_40%,transparent)_3px_6px)]";
