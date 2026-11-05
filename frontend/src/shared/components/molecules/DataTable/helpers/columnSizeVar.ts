/**
 * Column ids are user-facing strings (e.g. "Project team"), and characters outside the CSS ident
 * grammar make both the custom-property declaration and any `var()` reference to it invalid — the
 * cell's `flex` then falls back to content sizing and the column stops lining up with its header
 * and with the same column in other rows. Sanitize the id so the name is always a valid ident.
 */
function sanitizeColumnId(columnId: string): string {
  // Escaping `_` too keeps the encoding injective: "a b" and "a_b" must not collide, or two
  // columns would share one size variable.
  return columnId.replace(/[^a-zA-Z0-9-]/g, char => `_${char.codePointAt(0)?.toString(16)}_`);
}

export function headerSizeVarName(columnId: string): `--header-${string}-size` {
  return `--header-${sanitizeColumnId(columnId)}-size`;
}

export function columnSizeVarName(columnId: string): `--col-${string}-size` {
  return `--col-${sanitizeColumnId(columnId)}-size`;
}

/** CSS length expression for a column's size, for use in `width` / `flex-basis`. */
export function columnSizeCss(columnId: string): string {
  return `calc(var(${headerSizeVarName(columnId)}) * 1px)`;
}
