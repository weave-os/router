export function normalizeSearchText(value: string) {
  return value.trim().toLocaleLowerCase().normalize("NFC");
}

/** `word` with an "s" unless `count` is exactly one. */
export function pluralize(count: number, word: string) {
  return count === 1 ? word : `${word}s`;
}
